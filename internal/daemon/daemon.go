package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
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
	sched := NewScheduler(opt.Tasks, opt.Runner, opt.Location, opt.Log)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		v := map[string]any{}
		if opt.Status != nil {
			v = opt.Status()
		}
		v["tasks"] = sched.Status()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	})
	if opt.Metrics != nil {
		mux.Handle("GET /metrics", opt.Metrics)
	}
	srv := &http.Server{Addr: opt.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	opt.Log.Info("daemon started", "listen", opt.Listen, "tasks", len(opt.Tasks))

	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
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
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) && runErr == nil {
		runErr = err
	}
	cancel()
	if !sched.Wait(opt.Grace, cancelJobs) && runErr == nil {
		runErr = errors.New("running jobs were cancelled after the grace period")
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}
