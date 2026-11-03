package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cistern/internal/cron"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func every(t *testing.T, d string) cron.Schedule {
	s, err := cron.Parse("@every " + d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchedulerRunsAndSkipsOverlap(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	var active, maxActive int32
	release := make(chan struct{})
	run := func(ctx context.Context, job, op string) error {
		mu.Lock()
		counts[job+"/"+op]++
		mu.Unlock()
		if job == "slow" {
			n := atomic.AddInt32(&active, 1)
			for {
				m := atomic.LoadInt32(&maxActive)
				if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
					break
				}
			}
			<-release
			atomic.AddInt32(&active, -1)
			return errors.New("slow failed")
		}
		return nil
	}
	s := NewScheduler([]Task{
		{Job: "fast", Op: "backup", Expr: "@every 50ms", Schedule: every(t, "50ms")},
		{Job: "fast", Op: "verify", Expr: "@every 80ms", Schedule: every(t, "80ms")},
		{Job: "slow", Op: "backup", Expr: "@every 30ms", Schedule: every(t, "30ms")},
	}, run, time.UTC, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Loop(ctx, context.Background()); close(done) }()
	time.Sleep(450 * time.Millisecond)
	cancel()
	<-done
	close(release)
	s.Wait(time.Second, func() {})

	mu.Lock()
	defer mu.Unlock()
	if counts["fast/backup"] < 5 || counts["fast/verify"] < 3 {
		t.Errorf("counts: %v", counts)
	}
	if counts["slow/backup"] != 1 || maxActive != 1 {
		t.Errorf("slow job overlapped: runs=%d max concurrent=%d", counts["slow/backup"], maxActive)
	}
	var slow TaskStatus
	for _, st := range s.Status() {
		if st.Job == "slow" {
			slow = st
		}
	}
	if slow.Skipped < 5 || slow.LastError != "slow failed" || slow.Running {
		t.Errorf("slow status: %+v", slow)
	}
}

func TestWaitCancelsAfterGrace(t *testing.T) {
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	started := make(chan struct{})
	var cancelled atomic.Bool
	s := NewScheduler([]Task{{Job: "j", Op: "backup", Schedule: every(t, "10ms")}}, func(ctx context.Context, _, _ string) error {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		return ctx.Err()
	}, time.UTC, quiet())
	ctx, stop := context.WithCancel(context.Background())
	go s.Loop(ctx, jobCtx)
	<-started
	stop()
	t0 := time.Now()
	if s.Wait(100*time.Millisecond, cancelJobs) {
		t.Fatal("Wait reported success for a job that never finished")
	}
	if !cancelled.Load() || time.Since(t0) > 2*time.Second {
		t.Fatalf("job not cancelled after grace (cancelled=%v, %v)", cancelled.Load(), time.Since(t0))
	}
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestRunServesStatusAndWaitsForJobs(t *testing.T) {
	addr := freePort(t)
	finished := make(chan struct{})
	started := make(chan struct{}, 1)
	opt := Options{
		Listen: addr, Grace: 5 * time.Second, Log: quiet(), Location: time.UTC,
		Status: func() map[string]any { return map[string]any{"version": "test"} },
		Tasks:  []Task{{Job: "db", Op: "backup", Expr: "@every 50ms", Schedule: every(t, "50ms")}},
		Runner: func(ctx context.Context, _, _ string) error {
			select {
			case started <- struct{}{}:
			default:
			}
			time.Sleep(300 * time.Millisecond)
			select {
			case <-finished:
			default:
				close(finished)
			}
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, opt) }()
	<-started

	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Version string
		Tasks   []TaskStatus
	}
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Version != "test" || len(st.Tasks) != 1 || !st.Tasks[0].Running || st.Tasks[0].Next.IsZero() {
		t.Fatalf("status: %+v", st)
	}

	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("daemon exited before the running job finished")
	}
}
