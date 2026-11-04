package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/crypt"
	"cistern/internal/driver"
	"cistern/internal/manifest"
	"cistern/internal/metrics"
	"cistern/internal/notify"
	"cistern/internal/pipeline"
)

var ErrDrillUnsupported = errors.New("this driver does not support drills")

func NewPlanner(cfg *config.Config, repos *Repos, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{Cfg: cfg, Log: log, Metrics: metrics.NewRegistry(), notifier: notify.New(nil, log), now: time.Now, repos: repos}
}

func (e *Engine) Drill(ctx context.Context, jobName, ref string) (*admin.DrillResult, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	if !config.SupportsDrill(job.Driver) {
		return nil, ErrDrillUnsupported
	}
	if job.Drill == nil {
		return nil, fmt.Errorf("job %s has no drill section in its configuration", job.Name)
	}
	start := e.now()
	m, rep, err := e.drill(ctx, job, ref)
	if rep == nil {
		rep = &admin.DrillReport{Checks: []admin.DrillCheck{}}
	}
	rep.Target = job.Drill.Target
	failErr := err
	if err != nil {
		failErr = fmt.Errorf("drill %s: %w", job.Name, err)
	} else if f := rep.FirstFailure(); f != nil {
		msg := f.Message
		if msg == "" && f.Expected != "" {
			msg = "expected " + f.Expected + ", got " + f.Actual
		}
		failErr = fmt.Errorf("drill %s: check %q failed: %s", job.Name, f.Name, msg)
	} else if !rep.Passed() {
		failErr = fmt.Errorf("drill %s: no check ran", job.Name)
	}
	rec := e.newRecord(ctx, job, admin.OpDrill, start, failErr)
	rec.Target, rec.Drill = job.Drill.Target, rep
	if m != nil {
		rec.Snapshot = m.ID
	}
	e.writeRecord(ctx, job, rec)
	l := metrics.Job(job.Name)
	if failErr != nil {
		e.Metrics.Set(metrics.DrillLastFailure, l, unix(e.now()))
	} else {
		e.Metrics.Set(metrics.DrillLastSuccess, l, unix(e.now()))
	}
	ev := e.event(job, admin.OpDrill, start, failErr)
	ev.Snapshot = rec.Snapshot
	e.notifier.Send(ctx, ev)
	e.publish(ctx, job)
	run := rec.Run()
	return run.Drill, failErr
}

func drillParams(job *config.Job, t *config.RestoreTarget) driver.Params {
	params := driver.Params{}
	for k, v := range job.Params {
		params[k] = v
	}
	for k, v := range t.Set {
		params[k] = v
	}
	for _, k := range []string{"clean", "restore_globals", "overwrite", "restore_suffix"} {
		delete(params, k)
	}
	return params
}

func (e *Engine) drill(ctx context.Context, job *config.Job, ref string) (*manifest.Manifest, *admin.DrillReport, error) {
	t, ok := job.Target(job.Drill.Target)
	if !ok || t.IsProduction() {
		return nil, nil, fmt.Errorf("drill target %q must be a restore target with production: false", job.Drill.Target)
	}
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, nil, err
	}
	list, err := e.list(ctx, r, prefix, job.Name)
	if err != nil {
		return nil, nil, err
	}
	m, err := Resolve(list, ref)
	if err != nil {
		return nil, nil, err
	}
	if m.Driver != job.Driver {
		return m, nil, fmt.Errorf("snapshot %s was made by driver %q, job uses %q", m.ID, m.Driver, job.Driver)
	}
	var ids []*crypt.Identity
	if m.Encryption != pipeline.EncryptionNone {
		if ids, err = e.identities(); err != nil {
			return m, nil, err
		}
		if len(ids) == 0 {
			return m, nil, errors.New("snapshot is encrypted: set encryption.identity_file to the private key")
		}
	}
	drv, err := driver.New(m.Driver, drillParams(job, t))
	if err != nil {
		return m, nil, err
	}
	dr, ok := drv.(driver.Driller)
	if !ok {
		return m, nil, ErrDrillUnsupported
	}
	unlock, err := e.lock(ctx, r, prefix, job, admin.OpDrill, m.ID)
	if err != nil {
		return m, nil, err
	}
	defer unlock()

	spec := driver.DrillSpec{Job: job.Name, Suffix: job.Drill.RestoreSuffix, Keep: job.Drill.Keep}
	for _, c := range job.Drill.Checks {
		spec.Checks = append(spec.Checks, driver.DrillSQL{Name: c.Name, SQL: c.SQL, Database: c.Database, Min: c.Min, Max: c.Max, Equals: c.Equals})
	}
	log := e.logger(ctx).With("job", job.Name, "snapshot", m.ID)
	log.Info("drill started", "target", t.Name, "suffix", spec.Suffix, "checks", len(spec.Checks))
	prog := runInfo(ctx).Progress
	if prog != nil {
		var total int64
		for _, a := range m.Artifacts {
			total += a.PlainSize
		}
		prog.SetTotal(int64(len(m.Artifacts)), total)
	}
	dctx, cancel := context.WithTimeout(ctx, job.Timeout)
	defer cancel()
	rep, err := dr.Drill(dctx, &source{ctx: dctx, e: e, repo: r, prefix: prefix, m: m, ids: ids, progress: prog}, spec)
	if err != nil {
		log.Error("drill failed", "err", err)
		return m, rep, err
	}
	if f := rep.FirstFailure(); f != nil {
		log.Error("drill failed", "check", f.Name, "message", f.Message, "cleaned_up", rep.CleanedUp)
	} else {
		log.Info("drill passed", "checks", len(rep.Checks), "restored_bytes", rep.RestoredBytes, "tables", len(rep.Tables), "cleaned_up", rep.CleanedUp)
	}
	return m, rep, nil
}

func (e *Engine) DrillHistory(ctx context.Context, jobName string) ([]*admin.Record, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	recs, err := admin.ListRecords(ctx, r, prefix, job.Name)
	var out []*admin.Record
	for _, rec := range recs {
		if rec.Op == admin.OpDrill {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, err
}

func (e *Engine) refreshDrill(ctx context.Context, job *config.Job) {
	if job.Drill == nil {
		return
	}
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return
	}
	keys, err := admin.RecordKeys(ctx, r, prefix, job.Name)
	if err != nil {
		return
	}
	dir := admin.RunsDir(prefix, job.Name) + "/"
	l := metrics.Job(job.Name)
	_, haveOK := e.Metrics.Get(metrics.DrillLastSuccess, l)
	_, haveFail := e.Metrics.Get(metrics.DrillLastFailure, l)
	for i, read := len(keys)-1, 0; i >= 0 && read < 400 && !(haveOK && haveFail); i-- {
		if _, ok := admin.IDTime(strings.TrimPrefix(keys[i], dir)); !ok {
			continue
		}
		read++
		var rec admin.Record
		if admin.ReadJSON(ctx, r, keys[i], &rec) != nil || rec.Op != admin.OpDrill {
			continue
		}
		switch {
		case rec.Status == admin.StateSucceeded && !haveOK:
			e.Metrics.Set(metrics.DrillLastSuccess, l, unix(rec.FinishedAt))
			haveOK = true
		case rec.Status == admin.StateFailed && !haveFail:
			e.Metrics.Set(metrics.DrillLastFailure, l, unix(rec.FinishedAt))
			haveFail = true
		}
	}
}
