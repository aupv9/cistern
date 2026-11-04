package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

	Execute     Executor
	APIToken    string
	APIListen   string
	APICertFile string
	APIKeyFile  string
	Prepare     func(req *RunRequest) error
}

const DefaultAPIListen = ":9465"

func (o *Options) apiEnabled() bool { return o.APIToken != "" && o.Execute != nil }

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
	if opt.apiEnabled() && opt.APIListen == "" {
		opt.APIListen = DefaultAPIListen
	}
	if opt.apiEnabled() && opt.APIListen == opt.Listen {
		return fmt.Errorf("daemon.api_listen (%s) must differ from the metrics listener", opt.APIListen)
	}
	if (opt.APICertFile == "") != (opt.APIKeyFile == "") {
		return errors.New("daemon.api_tls: cert_file and key_file must be set together")
	}
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	d := &daemonState{opt: opt, reg: newRegistry(opt.Execute, opt.Log), jobCtx: jobCtx}

	runner := opt.Runner
	if opt.Execute != nil {
		runner = func(ctx context.Context, job, op string) error {
			req := &RunRequest{Job: job, Op: op, Trigger: admin.TriggerSchedule, RequestedBy: &admin.ActorScheduler}
			if opt.Prepare != nil {
				if err := opt.Prepare(req); err != nil {
					return err
				}
			}
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

	status := func(w http.ResponseWriter, _ *http.Request) {
		v := map[string]any{}
		if opt.Status != nil {
			v = opt.Status()
		}
		v["tasks"] = sched.Status()
		v["running"] = d.reg.running()
		v["timezone"] = sched.loc.String()
		v["api"] = opt.apiEnabled()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	}
	healthz := func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /status", status)
	if opt.Metrics != nil {
		mux.Handle("GET /metrics", opt.Metrics)
	}
	ln, err := net.Listen("tcp", opt.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()

	var apiSrv *http.Server
	if opt.apiEnabled() {
		apiMux := http.NewServeMux()
		apiMux.HandleFunc("GET /healthz", healthz)
		d.internalAPI(apiMux, status)
		apiSrv = &http.Server{Handler: apiMux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
		if opt.APICertFile != "" {
			cert, err := tls.LoadX509KeyPair(opt.APICertFile, opt.APIKeyFile)
			if err != nil {
				srv.Close()
				return fmt.Errorf("daemon.api_tls: %w", err)
			}
			apiSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		}
		aln, err := net.Listen("tcp", opt.APIListen)
		if err != nil {
			srv.Close()
			return fmt.Errorf("daemon.api_listen: %w", err)
		}
		hs, useTLS := apiSrv, apiSrv.TLSConfig != nil
		go func() {
			if useTLS {
				errc <- hs.ServeTLS(aln, "", "")
			} else {
				errc <- hs.Serve(aln)
			}
		}()
	}
	opt.Log.Info("daemon started", "listen", opt.Listen, "tasks", len(opt.Tasks), "internal_api", opt.apiEnabled(), "api_listen", apiListenLog(opt), "api_tls", opt.apiEnabled() && opt.APICertFile != "")

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
	for _, hs := range []*http.Server{srv, apiSrv} {
		if hs == nil {
			continue
		}
		if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) && runErr == nil {
			runErr = err
		}
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

func apiListenLog(opt Options) string {
	if !opt.apiEnabled() {
		return ""
	}
	return opt.APIListen
}
