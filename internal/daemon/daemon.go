package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cistern/internal/admin"
)

type Options struct {
	Listen   string
	Grace    time.Duration
	Metrics  http.Handler
	Status   func() map[string]any
	Tasks    []Task
	Runner   Runner
	Location *time.Location
	Log      *slog.Logger

	Execute  Executor
	APIToken string
	Prepare  func(req *RunRequest) error
}

type daemonState struct {
	opt    Options
	reg    *registry
	jobCtx context.Context
}

func Run(ctx context.Context, opt Options) error {
	if opt.Listen == "" {
		opt.Listen = ":9464"
	}
	if opt.Grace == 0 {
		opt.Grace = 5 * time.Minute
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	d := &daemonState{opt: opt, reg: newRegistry(opt.Execute, opt.Log), jobCtx: jobCtx}

	runner := opt.Runner
	if opt.Execute != nil {
		runner = func(ctx context.Context, job, op string) error {
			req := &RunRequest{Job: job, Op: op, Trigger: admin.TriggerSchedule, RequestedBy: &admin.ActorScheduler}
			e, err := d.reg.begin(req)
			if err != nil {
				return err
			}
			return d.reg.execute(ctx, e, req)
		}
	}
	sched := NewScheduler(opt.Tasks, runner, opt.Location, opt.Log)
	if opt.Execute != nil {
		sched.Busy = d.reg.isBusy
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		v := map[string]any{}
		if opt.Status != nil {
			v = opt.Status()
		}
		v["tasks"] = sched.Status()
		v["running"] = d.reg.running()
		v["timezone"] = sched.loc.String()
		v["api"] = opt.APIToken != "" && opt.Execute != nil
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	})
	if opt.Metrics != nil {
		mux.Handle("GET /metrics", opt.Metrics)
	}
	if opt.APIToken != "" && opt.Execute != nil {
		d.internalAPI(mux)
	}
	srv := &http.Server{Addr: opt.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	opt.Log.Info("daemon started", "listen", opt.Listen, "tasks", len(opt.Tasks), "internal_api", opt.APIToken != "" && opt.Execute != nil)

	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() { sched.Loop(loopCtx, jobCtx); close(loopDone) }()

	var runErr error
	select {
	case err := <-errc:
		runErr = err
	case <-ctx.Done():
		opt.Log.Info("shutting down")
	}
	stopLoop()
	<-loopDone
	d.reg.close()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) && runErr == nil {
		runErr = err
	}
	cancel()
	apiDone := make(chan struct{})
	go func() { d.reg.wg.Wait(); close(apiDone) }()
	deadline := time.Now().Add(opt.Grace)
	if !sched.Wait(opt.Grace, cancelJobs) && runErr == nil {
		runErr = errors.New("running jobs were cancelled after the grace period")
	}
	select {
	case <-apiDone:
	default:
		select {
		case <-apiDone:
		case <-time.After(time.Until(deadline)):
			opt.Log.Warn("grace period over, cancelling running jobs started from the admin server")
			cancelJobs()
			<-apiDone
			if runErr == nil {
				runErr = errors.New("running jobs were cancelled after the grace period")
			}
		}
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}
