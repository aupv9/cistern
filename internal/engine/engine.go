package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"cistern/internal/config"
	"cistern/internal/crypt"
	"cistern/internal/driver"
	"cistern/internal/manifest"
	"cistern/internal/metrics"
	"cistern/internal/notify"
	"cistern/internal/pipeline"
	"cistern/internal/repo"
)

type Engine struct {
	Cfg     *config.Config
	Version string
	Log     *slog.Logger
	Metrics *metrics.Registry

	Publish bool

	notifier *notify.Notifier

	now      func() time.Time
	hostname string

	mu    sync.Mutex
	repos map[string]repo.Repository
	ids   []*crypt.Identity
	idErr error
	idSet bool
}

func New(cfg *config.Config, version string, log *slog.Logger) (*Engine, error) {
	known := map[string]bool{}
	for _, n := range driver.Names() {
		known[n] = true
	}
	for _, j := range cfg.Jobs {
		if !known[j.Driver] {
			return nil, fmt.Errorf("job %s: unknown driver %q (available: %s)", j.Name, j.Driver, strings.Join(driver.Names(), ", "))
		}
	}
	host, _ := os.Hostname()
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		Cfg: cfg, Version: version, Log: log,
		Metrics:  metrics.NewRegistry(),
		notifier: notify.New(cfg.Notify.Webhooks, log),
		now:      time.Now, hostname: host, repos: map[string]repo.Repository{},
	}, nil
}

func (e *Engine) repoFor(job *config.Job) (repo.Repository, string, error) {
	rc := e.Cfg.Repositories[job.Repository]
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.repos[job.Repository]; ok {
		return r, rc.Prefix, nil
	}
	var r repo.Repository
	switch rc.Type {
	case "local":
		l, err := repo.NewLocal(e.Cfg.Resolve(rc.Path))
		if err != nil {
			return nil, "", fmt.Errorf("repository %s: %w", job.Repository, err)
		}
		r = l
	case "s3":
		cfg := repo.S3Config{
			Endpoint: rc.Endpoint, Region: rc.Region, Bucket: rc.Bucket, PathStyle: rc.PathStyle,
			AccessKeyID: rc.AccessKeyID, SecretAccessKey: rc.SecretAccessKey, SessionToken: rc.SessionToken,
			PartSizeMB: rc.PartSizeMB, CAFile: e.Cfg.Resolve(rc.CAFile), InsecureSkipVerify: rc.InsecureSkip,
			Log: e.Log.With("repository", job.Repository),
		}
		if rc.ObjectLock != nil {
			cfg.LockMode, cfg.LockDays = rc.ObjectLock.Mode, rc.ObjectLock.Days
		}
		s, err := repo.NewS3(cfg)
		if err != nil {
			return nil, "", fmt.Errorf("repository %s: %w", job.Repository, err)
		}
		r = s
	default:
		return nil, "", fmt.Errorf("repository %s: unknown type %q", job.Repository, rc.Type)
	}
	e.repos[job.Repository] = r
	return r, rc.Prefix, nil
}

func (e *Engine) identities() ([]*crypt.Identity, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.idSet {
		e.ids, e.idErr = e.Cfg.LoadIdentities()
		e.idSet = true
	}
	return e.ids, e.idErr
}

func (e *Engine) pipelineOptions() pipeline.Options {
	o := pipeline.Options{Compression: pipeline.CompressionNone, Recipients: e.Cfg.Recipients}
	if l := e.Cfg.CompressionLevel(); l > 0 {
		o.Compression, o.Level = pipeline.CompressionGzip, l
	}
	return o
}

func (e *Engine) lock(ctx context.Context, r repo.Repository, prefix string, job *config.Job, op, snap string) (func(), error) {
	key := manifest.LockKey(prefix, job.Name)
	now := e.now().UTC()
	data, _ := json.Marshal(manifest.Lock{
		Host: e.hostname, PID: os.Getpid(), Op: op, Snapshot: snap,
		Started: now, Expires: now.Add(job.Timeout + manifest.LockGrace),
	})
	for attempt := 0; attempt < 2; attempt++ {
		err := r.PutIfAbsent(ctx, key, data)
		if err == nil {
			return func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if err := r.Delete(ctx, key); err != nil {
					e.Log.Warn("releasing lock failed", "job", job.Name, "err", err)
				}
			}, nil
		}
		if !errors.Is(err, repo.ErrExist) {
			return nil, fmt.Errorf("acquiring lock: %w", err)
		}
		var held manifest.Lock
		if rc, gerr := r.Get(ctx, key); gerr == nil {
			derr := json.NewDecoder(rc).Decode(&held)
			rc.Close()
			if derr == nil && now.Before(held.Expires) {
				return nil, fmt.Errorf("job is locked: %s by %s (pid %d) since %s", held.Op, held.Host, held.PID, held.Started.Format(time.RFC3339))
			}
		}
		e.Log.Warn("removing expired lock", "job", job.Name, "holder", held.Host, "pid", held.PID, "expired", held.Expires)
		if err := r.Delete(ctx, key); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("could not acquire lock")
}

type sink struct {
	ctx               context.Context
	repo              repo.Repository
	prefix, job, snap string
	opts              pipeline.Options

	mu        sync.Mutex
	names     map[string]bool
	open      map[*artifactWriter]bool
	artifacts []manifest.Artifact

	previous func() driver.Source
	prevOnce sync.Once
	prev     driver.Source
}

type artifactWriter struct {
	s      *sink
	name   string
	path   string
	pw     *io.PipeWriter
	pl     *pipeline.Writer
	done   chan error
	closed bool
}

func (s *sink) Create(name string) (io.WriteCloser, error) {
	if err := manifest.ValidateArtifactName(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.names[name] {
		s.mu.Unlock()
		return nil, fmt.Errorf("artifact %q created twice", name)
	}
	s.names[name] = true
	s.mu.Unlock()

	suffix := manifest.ArtifactSuffix(s.opts.Compression == pipeline.CompressionGzip, len(s.opts.Recipients) > 0)
	key := manifest.ArtifactKey(s.prefix, s.job, s.snap, name, suffix)
	pr, pw := io.Pipe()
	aw := &artifactWriter{s: s, name: name, path: "artifacts/" + name + suffix, pw: pw, done: make(chan error, 1)}

	go func() {
		err := s.repo.Put(s.ctx, key, pr)
		if err != nil {
			pr.CloseWithError(err)
		}
		aw.done <- err
	}()
	pl, err := pipeline.NewWriter(pw, s.opts)
	if err != nil {
		pw.CloseWithError(err)
		<-aw.done
		return nil, err
	}
	aw.pl = pl
	s.mu.Lock()
	s.open[aw] = true
	s.mu.Unlock()
	return aw, nil
}

func (s *sink) Previous() driver.Source {
	s.prevOnce.Do(func() {
		if s.previous != nil {
			s.prev = s.previous()
		}
	})
	return s.prev
}

func (e *Engine) previousSource(ctx context.Context, r repo.Repository, prefix string, job *config.Job) driver.Source {
	list, err := e.list(ctx, r, prefix, job.Name)
	if err != nil {
		e.Log.Warn("incremental: listing snapshots failed; making a full copy", "job", job.Name, "err", err)
		return nil
	}
	for i := len(list) - 1; i >= 0; i-- {
		m := list[i]
		if m.Driver != job.Driver {
			continue
		}
		var ids []*crypt.Identity
		if m.Encryption != pipeline.EncryptionNone {
			if ids, err = e.identities(); err != nil || len(ids) == 0 {
				e.Log.Warn("incremental: reading the previous snapshot needs encryption.identity_file; making a full copy", "job", job.Name, "previous", m.ID)
				return nil
			}
		}
		return &source{ctx: ctx, e: e, repo: r, prefix: prefix, m: m, ids: ids}
	}
	return nil
}

func (a *artifactWriter) Write(p []byte) (int, error) { return a.pl.Write(p) }

func (a *artifactWriter) Close() error {
	if a.closed {
		return errors.New("artifact closed twice")
	}
	a.closed = true
	a.s.mu.Lock()
	delete(a.s.open, a)
	a.s.mu.Unlock()

	err := a.pl.Close()
	if err != nil {
		a.pw.CloseWithError(err)
	} else {
		a.pw.Close()
	}
	if uerr := <-a.done; err == nil {
		err = uerr
	}
	if err != nil {
		return fmt.Errorf("artifact %s: %w", a.name, err)
	}
	res := a.pl.Result()
	a.s.mu.Lock()
	a.s.artifacts = append(a.s.artifacts, manifest.Artifact{
		Name: a.name, Path: a.path,
		PlainSize: res.PlainSize, StoredSize: res.StoredSize,
		PlainSHA256: res.PlainSHA256, StoredSHA256: res.StoredSHA256,
	})
	a.s.mu.Unlock()
	return nil
}

func (s *sink) abort(cause error) {
	s.mu.Lock()
	open := make([]*artifactWriter, 0, len(s.open))
	for a := range s.open {
		open = append(open, a)
	}
	s.open = map[*artifactWriter]bool{}
	s.mu.Unlock()
	for _, a := range open {
		a.closed = true
		a.pw.CloseWithError(cause)
		<-a.done
	}
}

func (s *sink) unclosed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for a := range s.open {
		names = append(names, a.name)
	}
	sort.Strings(names)
	return names
}

func (e *Engine) Backup(ctx context.Context, jobName string) (*manifest.Manifest, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	start := e.now()
	m, err := e.backup(ctx, job)
	e.recordBackup(ctx, job, start, m, err)
	return m, err
}

func (e *Engine) backup(ctx context.Context, job *config.Job) (*manifest.Manifest, error) {
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	drv, err := driver.New(job.Driver, driver.Params(job.Params))
	if err != nil {
		return nil, err
	}
	started := e.now().UTC()
	snap := manifest.NewSnapshotID(started)
	log := e.Log.With("job", job.Name, "snapshot", snap)

	unlock, err := e.lock(ctx, r, prefix, job, "backup", snap)
	if err != nil {
		return nil, err
	}
	defer unlock()

	log.Info("backup started", "driver", job.Driver, "repository", job.Repository)
	bctx, cancel := context.WithTimeout(ctx, job.Timeout)
	defer cancel()
	opts := e.pipelineOptions()
	s := &sink{ctx: bctx, repo: r, prefix: prefix, job: job.Name, snap: snap, opts: opts,
		names: map[string]bool{}, open: map[*artifactWriter]bool{},
		previous: func() driver.Source { return e.previousSource(bctx, r, prefix, job) }}

	info, err := drv.Backup(bctx, s)
	if err == nil {
		if open := s.unclosed(); len(open) > 0 {
			err = fmt.Errorf("driver did not close artifacts: %s", strings.Join(open, ", "))
		} else if len(s.artifacts) == 0 {
			err = errors.New("driver produced no artifacts")
		}
	}
	if err == nil && bctx.Err() != nil {
		err = bctx.Err()
	}

	var m *manifest.Manifest
	if err == nil {
		sort.Slice(s.artifacts, func(i, j int) bool { return s.artifacts[i].Name < s.artifacts[j].Name })
		m = &manifest.Manifest{
			Format: manifest.Format, ID: snap, Job: job.Name, Driver: job.Driver, Status: "completed",
			StartedAt: started, FinishedAt: e.now().UTC(),
			Source:      manifest.Source{Version: info.Version, Host: info.Host, Extra: info.Extra},
			Compression: opts.Compression, Encryption: opts.Encryption(),
			DependsOn: info.DependsOn, Artifacts: s.artifacts,
			CisternVersion: e.Version, Hostname: e.hostname,
		}
		for _, rc := range opts.Recipients {
			m.Recipients = append(m.Recipients, rc.String())
		}
		data, _ := json.MarshalIndent(m, "", "  ")

		err = r.Put(bctx, manifest.ManifestKey(prefix, job.Name, snap), strings.NewReader(string(data)+"\n"))
	}
	if err != nil {
		s.abort(err)
		e.removeSnapshot(r, prefix, job.Name, snap, log)
		log.Error("backup failed", "err", err)
		return nil, fmt.Errorf("backup %s: %w", job.Name, err)
	}
	var stored int64
	for _, a := range m.Artifacts {
		stored += a.StoredSize
	}
	log.Info("backup completed", "artifacts", len(m.Artifacts), "stored_bytes", stored, "duration", m.FinishedAt.Sub(started).Round(time.Millisecond))

	if job.PruneAfterBackup != nil && *job.PruneAfterBackup {
		if rep, err := e.prune(ctx, r, prefix, job, false); err != nil {
			log.Warn("prune after backup failed", "err", err)
		} else if rep.Errors > 0 {
			log.Warn("prune after backup had errors", "errors", rep.Errors)
		}
	}
	return m, nil
}

func storedSize(m *manifest.Manifest) int64 {
	var n int64
	for _, a := range m.Artifacts {
		n += a.StoredSize
	}
	return n
}

func (e *Engine) event(job *config.Job, op string, start time.Time, err error) notify.Event {
	ev := notify.Event{Job: job.Name, Op: op, Status: notify.StatusSuccess, Host: e.hostname, StartedAt: start, FinishedAt: e.now()}
	if err != nil {
		ev.Status, ev.Error = notify.StatusFailure, err.Error()
	}
	return ev
}

func (e *Engine) recordBackup(ctx context.Context, job *config.Job, start time.Time, m *manifest.Manifest, err error) {
	l := metrics.Job(job.Name)
	ev := e.event(job, "backup", start, err)
	if err != nil {
		e.Metrics.Set(metrics.BackupLastFailure, l, unix(e.now()))
		e.Metrics.Add(metrics.BackupFailures, l, 1)
	} else {
		e.Metrics.Set(metrics.BackupLastSuccess, l, unix(m.FinishedAt))
		e.Metrics.Set(metrics.BackupDuration, l, m.FinishedAt.Sub(m.StartedAt).Seconds())
		e.refreshJob(ctx, job)
		ev.Snapshot, ev.Artifacts, ev.StoredBytes = m.ID, len(m.Artifacts), storedSize(m)
	}
	e.notifier.Send(ctx, ev)
	e.publish(ctx, job)
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func (e *Engine) refreshJob(ctx context.Context, job *config.Job) {
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return
	}
	list, err := e.list(ctx, r, prefix, job.Name)
	if err != nil {
		e.Log.Warn("refreshing metrics failed", "job", job.Name, "err", err)
		return
	}
	l := metrics.Job(job.Name)
	e.Metrics.Set(metrics.Snapshots, l, float64(len(list)))
	if n := len(list); n > 0 {
		e.Metrics.Set(metrics.BackupLastSize, l, float64(storedSize(list[n-1])))
		if n > 1 {
			e.Metrics.Set(metrics.BackupPreviousSize, l, float64(storedSize(list[n-2])))
		}
	}
}

func (e *Engine) RefreshMetrics(ctx context.Context) {
	e.Metrics.Set(metrics.BuildInfo, metrics.Labels{{"version", e.Version}}, 1)
	for _, job := range e.Cfg.Jobs {
		e.refreshJob(ctx, job)
		r, prefix, err := e.repoFor(job)
		if err != nil {
			continue
		}
		if list, err := e.list(ctx, r, prefix, job.Name); err == nil && len(list) > 0 {
			newest := list[len(list)-1]
			e.Metrics.Set(metrics.BackupLastSuccess, metrics.Job(job.Name), unix(newest.FinishedAt))
		}
	}
}

type JobStatus struct {
	Name              string     `json:"name"`
	Driver            string     `json:"driver"`
	Schedule          string     `json:"schedule,omitempty"`
	Snapshots         int        `json:"snapshots"`
	LastSuccess       *time.Time `json:"last_success,omitempty"`
	LastFailure       *time.Time `json:"last_failure,omitempty"`
	LastVerifySuccess *time.Time `json:"last_verify_success,omitempty"`
}

func (e *Engine) Status() map[string]any {
	ts := func(name, job string) *time.Time {
		v, ok := e.Metrics.Get(name, metrics.Job(job))
		if !ok {
			return nil
		}
		t := time.Unix(0, int64(v*1e9)).UTC()
		return &t
	}
	jobs := make([]JobStatus, 0, len(e.Cfg.Jobs))
	for _, j := range e.Cfg.Jobs {
		n, _ := e.Metrics.Get(metrics.Snapshots, metrics.Job(j.Name))
		jobs = append(jobs, JobStatus{
			Name: j.Name, Driver: j.Driver, Schedule: j.Schedule, Snapshots: int(n),
			LastSuccess:       ts(metrics.BackupLastSuccess, j.Name),
			LastFailure:       ts(metrics.BackupLastFailure, j.Name),
			LastVerifySuccess: ts(metrics.VerifyLastSuccess, j.Name),
		})
	}
	return map[string]any{"version": e.Version, "host": e.hostname, "jobs": jobs}
}

func (e *Engine) publish(ctx context.Context, job *config.Job) {
	if !e.Publish {
		return
	}
	if u := e.Cfg.Metrics.PushgatewayURL; u != "" {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := e.Metrics.Push(pctx, u, job.Name); err != nil {
			e.Log.Warn("pushgateway failed", "job", job.Name, "err", err)
		}
	}
	if dir := e.Cfg.Metrics.TextfileDir; dir != "" {
		if err := e.Metrics.WriteTextfile(e.Cfg.Resolve(dir), job.Name); err != nil {
			e.Log.Warn("writing metrics textfile failed", "job", job.Name, "err", err)
		}
	}
}

func (e *Engine) removeSnapshot(r repo.Repository, prefix, job, snap string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	keys, err := r.List(ctx, manifest.SnapshotDir(prefix, job, snap)+"/")
	if err != nil {
		log.Warn("cleanup: listing snapshot failed", "err", err)
		return
	}
	for _, k := range keys {
		if err := r.Delete(ctx, k); err != nil {
			log.Warn("cleanup: delete failed", "key", k, "err", err)
		}
	}
}

func (e *Engine) List(ctx context.Context, jobName string) ([]*manifest.Manifest, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	return e.list(ctx, r, prefix, job.Name)
}

func (e *Engine) list(ctx context.Context, r repo.Repository, prefix, job string) ([]*manifest.Manifest, error) {
	jobDir := path.Join(prefix, job) + "/"
	keys, err := r.List(ctx, jobDir)
	if err != nil {
		return nil, err
	}
	var out []*manifest.Manifest
	for _, k := range keys {
		id, rest, ok := strings.Cut(strings.TrimPrefix(k, jobDir), "/")
		if !ok || rest != "manifest.json" || !manifest.ValidSnapshotID(id) {
			continue
		}
		m, err := readManifest(ctx, r, k)
		if err != nil {
			e.Log.Warn("skipping unreadable manifest", "key", k, "err", err)
			continue
		}
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func readManifest(ctx context.Context, r repo.Repository, key string) (*manifest.Manifest, error) {
	rc, err := r.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var m manifest.Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return nil, err
	}
	if m.Format != manifest.Format {
		return nil, fmt.Errorf("unsupported manifest format %d", m.Format)
	}
	return &m, nil
}

func resolve(list []*manifest.Manifest, ref string) (*manifest.Manifest, error) {
	if len(list) == 0 {
		return nil, errors.New("no snapshots")
	}
	if ref == "" || ref == "latest" {
		return list[len(list)-1], nil
	}
	var matches []*manifest.Manifest
	for _, m := range list {
		if m.ID == ref {
			return m, nil
		}
		if strings.HasPrefix(m.ID, ref) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("snapshot %q not found", ref)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.ID
	}
	return nil, fmt.Errorf("snapshot prefix %q is ambiguous: %s", ref, strings.Join(ids, ", "))
}

type source struct {
	ctx    context.Context
	e      *Engine
	repo   repo.Repository
	prefix string
	m      *manifest.Manifest
	ids    []*crypt.Identity
}

func (s *source) SnapshotID() string { return s.m.ID }

func (s *source) Snapshot(id string) (driver.Source, error) {
	if id == s.m.ID {
		return s, nil
	}
	allowed := false
	for _, d := range s.m.DependsOn {
		allowed = allowed || d == id
	}
	if !allowed {
		return nil, fmt.Errorf("snapshot %s is not in depends_on of %s", id, s.m.ID)
	}
	dep, err := readManifest(s.ctx, s.repo, manifest.ManifestKey(s.prefix, s.m.Job, id))
	if err != nil {
		return nil, fmt.Errorf("dependency snapshot %s: %w", id, err)
	}
	ids := s.ids
	if dep.Encryption != pipeline.EncryptionNone && len(ids) == 0 && s.e != nil {
		if ids, err = s.e.identities(); err != nil {
			return nil, err
		}
	}
	return &source{ctx: s.ctx, e: s.e, repo: s.repo, prefix: s.prefix, m: dep, ids: ids}, nil
}

func (s *source) List() []string {
	names := make([]string, len(s.m.Artifacts))
	for i, a := range s.m.Artifacts {
		names[i] = a.Name
	}
	return names
}

func (s *source) Open(name string) (io.ReadCloser, error) {
	for _, a := range s.m.Artifacts {
		if a.Name == name {
			return openArtifact(s.ctx, s.repo, s.prefix, s.m, a, s.ids)
		}
	}
	return nil, fmt.Errorf("artifact %q not in snapshot %s", name, s.m.ID)
}

func artifactKey(prefix string, m *manifest.Manifest, a manifest.Artifact) string {
	return path.Join(manifest.SnapshotDir(prefix, m.Job, m.ID), a.Path)
}

func expectOf(a manifest.Artifact) pipeline.Result {
	return pipeline.Result{PlainSize: a.PlainSize, StoredSize: a.StoredSize, PlainSHA256: a.PlainSHA256, StoredSHA256: a.StoredSHA256}
}

func openArtifact(ctx context.Context, r repo.Repository, prefix string, m *manifest.Manifest, a manifest.Artifact, ids []*crypt.Identity) (io.ReadCloser, error) {
	rc, err := r.Get(ctx, artifactKey(prefix, m, a))
	if err != nil {
		return nil, err
	}
	pr, err := pipeline.NewReader(rc, pipeline.ReadOptions{
		Compression: m.Compression, Encryption: m.Encryption, Identities: ids, Expect: expectOf(a),
	})
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("artifact %s: %w", a.Name, err)
	}
	return &artifactReader{Reader: pr, raw: rc, name: a.Name}, nil
}

type artifactReader struct {
	*pipeline.Reader
	raw    io.Closer
	name   string
	closed bool
	err    error
}

func (a *artifactReader) Close() error {
	if a.closed {
		return a.err
	}
	a.closed = true
	err := a.Reader.Close()
	a.raw.Close()
	if err != nil {
		a.err = fmt.Errorf("artifact %s: %w", a.name, err)
	}
	return a.err
}

func (a *artifactReader) Abort() {
	if !a.closed {
		a.closed = true
		a.raw.Close()
	}
}

func (e *Engine) Restore(ctx context.Context, jobName, ref string, overrides map[string]string) (*manifest.Manifest, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	start := e.now()
	m, err := e.restore(ctx, job, ref, overrides)
	ev := e.event(job, "restore", start, err)
	if m != nil {
		ev.Snapshot = m.ID
	}
	if err == nil {

		e.Metrics.Set(metrics.VerifyLastSuccess, metrics.Job(job.Name), unix(e.now()))
	}
	e.notifier.Send(ctx, ev)
	e.publish(ctx, job)
	return m, err
}

func (e *Engine) restore(ctx context.Context, job *config.Job, ref string, overrides map[string]string) (*manifest.Manifest, error) {
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	list, err := e.list(ctx, r, prefix, job.Name)
	if err != nil {
		return nil, err
	}
	m, err := resolve(list, ref)
	if err != nil {
		return nil, err
	}
	if m.Driver != job.Driver {
		return nil, fmt.Errorf("snapshot %s was made by driver %q, job uses %q", m.ID, m.Driver, job.Driver)
	}
	var ids []*crypt.Identity
	if m.Encryption != pipeline.EncryptionNone {
		if ids, err = e.identities(); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, errors.New("snapshot is encrypted: set encryption.identity_file to the private key")
		}
	}

	params := driver.Params{}
	for k, v := range job.Params {
		params[k] = v
	}
	for k, v := range overrides {
		params[k] = v
	}
	drv, err := driver.New(m.Driver, params)
	if err != nil {
		return nil, err
	}
	log := e.Log.With("job", job.Name, "snapshot", m.ID)
	log.Info("restore started")
	rctx, cancel := context.WithTimeout(ctx, job.Timeout)
	defer cancel()
	if err := drv.Restore(rctx, &source{ctx: rctx, e: e, repo: r, prefix: prefix, m: m, ids: ids}); err != nil {
		log.Error("restore failed", "err", err)
		return m, fmt.Errorf("restore %s from %s: %w", job.Name, m.ID, err)
	}
	log.Info("restore completed")
	return m, nil
}

type ArtifactResult struct {
	Snapshot string `json:"snapshot"`
	Artifact string `json:"artifact"`
	Mode     string `json:"mode"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type VerifyReport struct {
	Job      string           `json:"job"`
	Results  []ArtifactResult `json:"results"`
	Failures int              `json:"failures"`
}

func (e *Engine) Verify(ctx context.Context, jobName, ref string) (*VerifyReport, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	start := e.now()
	rep, err := e.verify(ctx, job, ref)
	l := metrics.Job(job.Name)
	failErr := err
	if err == nil && rep.Failures > 0 {
		var first string
		for _, r := range rep.Results {
			if !r.OK {
				first = r.Snapshot + " " + r.Artifact + ": " + r.Error
				break
			}
		}
		failErr = fmt.Errorf("%d artifact(s) failed verification; first: %s", rep.Failures, first)
	}
	if failErr != nil {
		e.Metrics.Set(metrics.VerifyLastFailure, l, unix(e.now()))
		n := 1
		if rep != nil && rep.Failures > 0 {
			n = rep.Failures
		}
		e.Metrics.Add(metrics.VerifyFailures, l, float64(n))
	} else {
		e.Metrics.Set(metrics.VerifyLastSuccess, l, unix(e.now()))
	}
	e.notifier.Send(ctx, e.event(job, "verify", start, failErr))
	e.publish(ctx, job)
	return rep, err
}

func (e *Engine) verify(ctx context.Context, job *config.Job, ref string) (*VerifyReport, error) {
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	list, err := e.list(ctx, r, prefix, job.Name)
	if err != nil {
		return nil, err
	}
	targets := list
	if ref != "all" {
		m, err := resolve(list, ref)
		if err != nil {
			return nil, err
		}
		targets = []*manifest.Manifest{m}
	}
	ids, idErr := e.identities()
	if idErr != nil {
		return nil, idErr
	}
	byID := map[string]bool{}
	for _, m := range list {
		byID[m.ID] = true
	}

	rep := &VerifyReport{Job: job.Name}
	for _, m := range targets {
		for _, dep := range m.DependsOn {
			if !byID[dep] {
				rep.Results = append(rep.Results, ArtifactResult{Snapshot: m.ID, Artifact: "depends_on:" + dep, Mode: "manifest", Error: "dependency snapshot missing"})
			}
		}
		full := m.Encryption == pipeline.EncryptionNone || len(ids) > 0
		for _, a := range m.Artifacts {
			res := ArtifactResult{Snapshot: m.ID, Artifact: a.Name, Mode: "stored"}
			if full {
				res.Mode = "full"
			}
			if err := e.verifyArtifact(ctx, r, prefix, m, a, ids, full); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
			rep.Results = append(rep.Results, res)
		}
	}
	for _, res := range rep.Results {
		if !res.OK {
			rep.Failures++
		}
	}
	return rep, nil
}

func (e *Engine) verifyArtifact(ctx context.Context, r repo.Repository, prefix string, m *manifest.Manifest, a manifest.Artifact, ids []*crypt.Identity, full bool) error {
	if !full {
		rc, err := r.Get(ctx, artifactKey(prefix, m, a))
		if err != nil {
			return err
		}
		defer rc.Close()
		return pipeline.VerifyStored(rc, expectOf(a))
	}
	rc, err := openArtifact(ctx, r, prefix, m, a, ids)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		rc.Close()
		return err
	}
	return rc.Close()
}

func (e *Engine) Check(ctx context.Context, jobName string) error {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return err
	}
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return err
	}
	if c, ok := r.(repo.Checker); ok {
		if err := c.Check(ctx); err != nil {
			return fmt.Errorf("repository %s: %w", job.Repository, err)
		}
	}
	if _, err := r.List(ctx, path.Join(prefix, job.Name)+"/"); err != nil {
		return fmt.Errorf("repository %s: %w", job.Repository, err)
	}
	drv, err := driver.New(job.Driver, driver.Params(job.Params))
	if err != nil {
		return err
	}
	return drv.Check(ctx)
}
