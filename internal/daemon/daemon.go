// Package daemon: scheduler và HTTP /metrics, /healthz, /status.
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
	Listen  string        // mặc định :9464
	Grace   time.Duration // thời gian chờ job đang chạy khi nhận SIGTERM, mặc định 5 phút
	Metrics http.Handler  // /metrics
	Status  func() any    // /status, mã hóa JSON
}

// Run phục vụ HTTP đến khi ctx bị hủy. TODO: scheduler chạy job theo cron.
func Run(ctx context.Context, opt Options) error {
	if opt.Listen == "" {
		opt.Listen = ":9464"
	}
	if opt.Grace == 0 {
		opt.Grace = 5 * time.Minute
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var v any = map[string]any{"jobs": []any{}}
		if opt.Status != nil {
			v = opt.Status()
		}
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
	slog.Info("daemon started", "listen", opt.Listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), opt.Grace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
