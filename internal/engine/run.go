package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/repo"
)

type RunInfo struct {
	ID          string
	Trigger     string
	RequestedBy *admin.Actor
	ApprovalID  string
	Target      string
	Log         *slog.Logger
	Progress    *admin.Progress
}

type runKey struct{}

func WithRun(ctx context.Context, ri *RunInfo) context.Context {
	return context.WithValue(ctx, runKey{}, ri)
}

var noRun = &RunInfo{}

func runInfo(ctx context.Context) *RunInfo {
	if ri, ok := ctx.Value(runKey{}).(*RunInfo); ok && ri != nil {
		return ri
	}
	return noRun
}

func (e *Engine) logger(ctx context.Context) *slog.Logger {
	if l := runInfo(ctx).Log; l != nil {
		return l
	}
	return e.Log
}

func (e *Engine) newRecord(ctx context.Context, job *config.Job, op string, start time.Time, err error) *admin.Record {
	ri := runInfo(ctx)
	end := e.now()
	rec := &admin.Record{
		ID: ri.ID, Job: job.Name, Op: op, Status: admin.StateSucceeded, Trigger: ri.Trigger,
		RequestedBy: ri.RequestedBy, ApprovalID: ri.ApprovalID, Target: ri.Target,
		StartedAt: start.UTC(), FinishedAt: end.UTC(), DurationMS: end.Sub(start).Milliseconds(),
	}
	if rec.ID == "" {
		rec.ID = admin.NewID(start)
	}
	if rec.Trigger == "" {
		rec.Trigger = admin.TriggerCLI
	}
	if rec.RequestedBy == nil {
		var a admin.Actor
		switch rec.Trigger {
		case admin.TriggerSchedule:
			a = admin.ActorScheduler
		default:
			a = admin.ActorCLI()
		}
		rec.RequestedBy = &a
	}
	if err != nil {
		rec.Status, rec.Error = admin.StateFailed, err.Error()
	}
	return rec
}

func (e *Engine) writeRecord(ctx context.Context, job *config.Job, rec *admin.Record) {
	r, prefix, err := e.repoFor(job)
	if err == nil {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err = admin.WriteRecord(wctx, r, prefix, rec)
		cancel()
	}
	if err != nil {
		e.logger(ctx).Warn("writing run record failed", "job", job.Name, "op", rec.Op, "run", rec.ID, "err", err)
	}
}

type Repos struct {
	cfg *config.Config
	log *slog.Logger

	mu sync.Mutex
	m  map[string]repo.Repository
}

func NewRepos(cfg *config.Config, log *slog.Logger) *Repos {
	if log == nil {
		log = slog.Default()
	}
	return &Repos{cfg: cfg, log: log, m: map[string]repo.Repository{}}
}

func (rs *Repos) Open(name string) (repo.Repository, string, error) {
	rc := rs.cfg.Repositories[name]
	if rc == nil {
		return nil, "", fmt.Errorf("repository %q not configured", name)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if r, ok := rs.m[name]; ok {
		return r, rc.Prefix, nil
	}
	var r repo.Repository
	switch rc.Type {
	case "local":
		l, err := repo.NewLocal(rs.cfg.Resolve(rc.Path))
		if err != nil {
			return nil, "", fmt.Errorf("repository %s: %w", name, err)
		}
		r = l
	case "s3":
		cfg := repo.S3Config{
			Endpoint: rc.Endpoint, Region: rc.Region, Bucket: rc.Bucket, PathStyle: rc.PathStyle,
			AccessKeyID: rc.AccessKeyID, SecretAccessKey: rc.SecretAccessKey, SessionToken: rc.SessionToken,
			PartSizeMB: rc.PartSizeMB, CAFile: rs.cfg.Resolve(rc.CAFile), InsecureSkipVerify: rc.InsecureSkip,
			Log: rs.log.With("repository", name),
		}
		if rc.ObjectLock != nil {
			cfg.LockMode, cfg.LockDays = rc.ObjectLock.Mode, rc.ObjectLock.Days
		}
		s, err := repo.NewS3(cfg)
		if err != nil {
			return nil, "", fmt.Errorf("repository %s: %w", name, err)
		}
		r = s
	default:
		return nil, "", fmt.Errorf("repository %s: unknown type %q", name, rc.Type)
	}
	rs.m[name] = r
	return r, rc.Prefix, nil
}
