package server

import (
	"context"
	"log/slog"
	"path"
	"sort"
	"sync"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/engine"
	"cistern/internal/manifest"
)

const (
	listingTTL = 30 * time.Second
	statusTTL  = 5 * time.Second
	fetchers   = 16
)

type jobData struct {
	snaps   []*manifest.Manifest
	runs    []*admin.Record
	err     error
	fetched time.Time
}

type jobEntry struct {
	mu         sync.Mutex
	data       *jobData
	refreshing bool
}

type store struct {
	cfg   *config.Config
	repos *engine.Repos
	log   *slog.Logger
	now   func() time.Time

	mu        sync.Mutex
	jobs      map[string]*jobEntry
	manifests map[string]*manifest.Manifest
	records   map[string]*admin.Record
	sem       chan struct{}
}

func newStore(cfg *config.Config, repos *engine.Repos, log *slog.Logger) *store {
	return &store{
		cfg: cfg, repos: repos, log: log, now: time.Now,
		jobs: map[string]*jobEntry{}, manifests: map[string]*manifest.Manifest{}, records: map[string]*admin.Record{},
		sem: make(chan struct{}, fetchers),
	}
}

func (s *store) entry(job string) *jobEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.jobs[job]
	if e == nil {
		e = &jobEntry{}
		s.jobs[job] = e
	}
	return e
}

func (s *store) invalidate(job string) {
	e := s.entry(job)
	e.mu.Lock()
	if e.data != nil {
		e.data.fetched = time.Time{}
	}
	e.mu.Unlock()
}

func (s *store) job(ctx context.Context, job *config.Job) *jobData {
	e := s.entry(job.Name)
	e.mu.Lock()
	if e.data != nil && s.now().Sub(e.data.fetched) >= listingTTL && !e.refreshing && e.data.err == nil {
		e.refreshing = true
		stale := e.data
		e.mu.Unlock()
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.refresh(rctx, job, e)
		}()
		return stale
	}
	e.mu.Unlock()
	return s.jobSync(ctx, job)
}

func (s *store) jobSync(ctx context.Context, job *config.Job) *jobData {
	e := s.entry(job.Name)
	e.mu.Lock()
	if e.data != nil && s.now().Sub(e.data.fetched) < listingTTL {
		d := e.data
		e.mu.Unlock()
		return d
	}
	e.refreshing = true
	e.mu.Unlock()
	return s.refresh(ctx, job, e)
}

func (s *store) refresh(ctx context.Context, job *config.Job, e *jobEntry) *jobData {
	s.sem <- struct{}{}
	d := s.fetch(ctx, job)
	<-s.sem
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshing = false
	if d.err != nil && e.data != nil && e.data.err == nil {
		s.log.Warn("refreshing job data failed; serving cached data", "job", job.Name, "err", d.err)
		e.data.fetched = s.now().Add(-listingTTL + 5*time.Second)
		return e.data
	}
	if e.data == nil || !d.fetched.Before(e.data.fetched) {
		e.data = d
	}
	return e.data
}

func (s *store) all(ctx context.Context) map[string]*jobData {
	out := make(map[string]*jobData, len(s.cfg.Jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, j := range s.cfg.Jobs {
		wg.Add(1)
		go func(j *config.Job) {
			defer wg.Done()
			d := s.job(ctx, j)
			mu.Lock()
			out[j.Name] = d
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	return out
}

func (s *store) fetch(ctx context.Context, job *config.Job) *jobData {
	d := &jobData{fetched: s.now()}
	r, prefix, err := s.repos.Open(job.Repository)
	if err != nil {
		d.err = err
		return d
	}
	jobDir := path.Join(prefix, job.Name) + "/"
	keys, err := r.List(ctx, jobDir)
	if err != nil {
		d.err = err
		return d
	}
	for _, k := range engine.ManifestKeys(keys, jobDir) {
		cacheKey := job.Repository + "\x00" + k
		s.mu.Lock()
		m := s.manifests[cacheKey]
		s.mu.Unlock()
		if m == nil {
			m, err = engine.ReadManifest(ctx, r, k)
			if err != nil {
				s.log.Warn("skipping unreadable manifest", "key", k, "err", err)
				continue
			}
			s.mu.Lock()
			s.manifests[cacheKey] = m
			s.mu.Unlock()
		}
		d.snaps = append(d.snaps, m)
	}
	engine.SortSnapshots(d.snaps)

	rkeys, err := admin.RecordKeys(ctx, r, prefix, job.Name)
	if err != nil {
		d.err = err
		return d
	}
	for _, k := range rkeys {
		cacheKey := job.Repository + "\x00" + k
		s.mu.Lock()
		rec := s.records[cacheKey]
		s.mu.Unlock()
		if rec == nil {
			var v admin.Record
			if err := admin.ReadJSON(ctx, r, k, &v); err != nil {
				s.log.Warn("skipping unreadable run record", "key", k, "err", err)
				continue
			}
			rec = &v
			s.mu.Lock()
			s.records[cacheKey] = rec
			s.mu.Unlock()
		}
		d.runs = append(d.runs, rec)
	}
	sort.Slice(d.runs, func(i, j int) bool {
		if !d.runs[i].StartedAt.Equal(d.runs[j].StartedAt) {
			return d.runs[i].StartedAt.Before(d.runs[j].StartedAt)
		}
		return d.runs[i].ID < d.runs[j].ID
	})
	return d
}

type daemonView struct {
	configured bool
	reachable  bool
	status     *daemon.Status
	err        error
}

type daemonCache struct {
	client *daemon.Client
	now    func() time.Time

	mu      sync.Mutex
	view    daemonView
	fetched time.Time
	down    time.Time
}

func (c *daemonCache) downSince() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down.IsZero() {
		return c.now().UTC()
	}
	return c.down.UTC()
}

func (c *daemonCache) get(ctx context.Context, fresh bool) daemonView {
	if c.client == nil {
		return daemonView{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !fresh && !c.fetched.IsZero() && c.now().Sub(c.fetched) < statusTTL {
		return c.view
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := c.client.Status(sctx)
	c.view = daemonView{configured: true, reachable: err == nil && st.API, status: st, err: err}
	if err == nil && !st.API {
		c.view.err = daemon.ErrUnavailable
	}
	c.fetched = c.now()
	if c.view.reachable {
		c.down = time.Time{}
	} else if c.down.IsZero() {
		c.down = c.fetched
	}
	return c.view
}

func (v daemonView) running(job string) *daemon.RunningOp {
	if v.status == nil {
		return nil
	}
	for i := range v.status.Running {
		if v.status.Running[i].Job == job {
			return &v.status.Running[i]
		}
	}
	return nil
}

func (v daemonView) next(job, op string) *time.Time {
	if v.status == nil {
		return nil
	}
	for _, t := range v.status.Tasks {
		if t.Job == job && t.Op == op && !t.Next.IsZero() {
			n := t.Next.UTC()
			return &n
		}
	}
	return nil
}
