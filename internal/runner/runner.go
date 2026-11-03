package runner

import (
	"context"
	"fmt"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/engine"
)

func Prepare(cfg *config.Config, req *daemon.RunRequest) error {
	job, err := cfg.Job(req.Job)
	if err != nil {
		return fmt.Errorf("%w: job %q", daemon.ErrNotFound, req.Job)
	}
	if req.Op != "restore" {
		if req.Target != "" || len(req.Overrides) > 0 || req.RestoreSuffix != nil {
			return &daemon.InvalidError{Message: "target, overrides and restore_suffix are only allowed for restore"}
		}
		return nil
	}
	if req.Target == "" {
		return &daemon.InvalidError{Message: "target is required", Fields: map[string]string{"target": "required"}}
	}
	own, _, err := job.RestoreOverrides(req.Target, req.RestoreSuffix)
	if err != nil {
		return &daemon.InvalidError{Message: err.Error(), Fields: map[string]string{"target": err.Error()}}
	}
	if !sameOverrides(own, req.Overrides) {
		return &daemon.InvalidError{Message: "the restore target differs between the server and the daemon configuration", Fields: map[string]string{"target": "configuration mismatch"}}
	}
	req.Overrides = own
	return nil
}

func sameOverrides(daemonSide, serverSide map[string]string) bool {
	if len(daemonSide) != len(serverSide) {
		return false
	}
	for k, v := range daemonSide {
		got, ok := serverSide[k]
		if !ok || (!admin.IsSecretKey(k) && got != v) {
			return false
		}
	}
	return true
}

func Execute(ctx context.Context, e *engine.Engine, req *daemon.RunRequest) (daemon.Result, error) {
	ctx = engine.WithRun(ctx, &engine.RunInfo{
		ID: req.RunID, Trigger: req.Trigger, RequestedBy: req.RequestedBy, ApprovalID: req.ApprovalID,
		Target: req.Target, Log: req.Log, Progress: req.Progress,
	})
	switch req.Op {
	case "backup":
		m, err := e.Backup(ctx, req.Job)
		if err != nil {
			return daemon.Result{}, err
		}
		var stored int64
		for _, a := range m.Artifacts {
			stored += a.StoredSize
		}
		return daemon.Result{Snapshot: m.ID, StoredBytes: stored}, nil
	case "verify":
		snap := req.Snapshot
		if snap == "" {
			snap = "latest"
		}
		rep, err := e.Verify(ctx, req.Job, snap)
		var res daemon.Result
		if rep != nil && len(rep.Results) > 0 && snap != "all" {
			res.Snapshot = rep.Results[0].Snapshot
		}
		if err == nil && rep.Failures > 0 {
			err = fmt.Errorf("verify: %d artifact(s) failed", rep.Failures)
		}
		return res, err
	case "restore":
		m, err := e.Restore(ctx, req.Job, req.Snapshot, req.Overrides)
		var res daemon.Result
		if m != nil {
			res.Snapshot = m.ID
		}
		return res, err
	}
	return daemon.Result{}, fmt.Errorf("unknown op %q", req.Op)
}
