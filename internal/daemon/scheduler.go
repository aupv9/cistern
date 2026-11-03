package daemon

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"cistern/internal/cron"
)

type Task struct {
	Job      string
	Op       string
	Expr     string
	Schedule cron.Schedule
}

type Runner func(ctx context.Context, job, op string) error

type TaskStatus struct {
	Job          string     `json:"job"`
	Op           string     `json:"op"`
	Schedule     string     `json:"schedule"`
	Next         time.Time  `json:"next"`
	Running      bool       `json:"running"`
	LastStarted  *time.Time `json:"last_started,omitempty"`
	LastFinished *time.Time `json:"last_finished,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Skipped      int        `json:"skipped"`
}

type task struct {
	Task
	next           time.Time
	running        bool
	started, ended time.Time
	lastErr        string
	skipped        int
}

type Scheduler struct {
	Busy func(job string) bool

	run Runner
	loc *time.Location
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	tasks []*task
	wg    sync.WaitGroup
}

func NewScheduler(tasks []Task, run Runner, loc *time.Location, log *slog.Logger) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	s := &Scheduler{run: run, loc: loc, log: log, now: time.Now}
	for _, t := range tasks {
		s.tasks = append(s.tasks, &task{Task: t})
	}
	return s
}

func (s *Scheduler) Loop(ctx, jobCtx context.Context) {
	now := s.now().In(s.loc)
	s.mu.Lock()
	for _, t := range s.tasks {
		t.next = t.Schedule.Next(now)
		s.log.Info("scheduled", "job", t.Job, "op", t.Op, "schedule", t.Expr, "next", t.next)
	}
	s.mu.Unlock()

	for {
		wait := s.dispatch(jobCtx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Scheduler) dispatch(jobCtx context.Context) time.Duration {
	now := s.now().In(s.loc)
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := time.Hour
	for _, t := range s.tasks {
		if t.next.IsZero() {
			continue
		}
		if !now.Before(t.next) {
			if t.running {
				t.skipped++
				s.log.Warn("skipping scheduled run: previous run still in progress", "job", t.Job, "op", t.Op, "started", t.started)
			} else if s.Busy != nil && s.Busy(t.Job) {
				t.skipped++
				s.log.Warn("skipping scheduled run: the job has another operation running", "job", t.Job, "op", t.Op)
			} else {
				s.start(jobCtx, t)
			}
			t.next = t.Schedule.Next(now)
			if t.next.IsZero() {
				continue
			}
		}
		if d := t.next.Sub(now); d < wait {
			wait = d
		}
	}
	if wait < 0 {
		wait = 0
	}
	return wait
}

func (s *Scheduler) start(jobCtx context.Context, t *task) {
	t.running, t.started = true, s.now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.run(jobCtx, t.Job, t.Op)
		s.mu.Lock()
		t.running, t.ended = false, s.now()
		t.lastErr = ""
		if err != nil {
			t.lastErr = err.Error()
		}
		s.mu.Unlock()
		if err != nil {
			s.log.Error("scheduled run failed", "job", t.Job, "op", t.Op, "err", err)
		}
	}()
}

func (s *Scheduler) Wait(grace time.Duration, cancel context.CancelFunc) bool {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	if running := s.runningTasks(); len(running) > 0 {
		s.log.Info("waiting for running jobs", "jobs", running, "grace", grace)
	}
	select {
	case <-done:
		return true
	case <-time.After(grace):
		s.log.Warn("grace period over, cancelling running jobs", "jobs", s.runningTasks())
		cancel()
		<-done
		return false
	}
}

func (s *Scheduler) runningTasks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, t := range s.tasks {
		if t.running {
			out = append(out, t.Job+"/"+t.Op)
		}
	}
	return out
}

func (s *Scheduler) Status() []TaskStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskStatus, 0, len(s.tasks))
	for _, t := range s.tasks {
		st := TaskStatus{Job: t.Job, Op: t.Op, Schedule: t.Expr, Next: t.next, Running: t.running, LastError: t.lastErr, Skipped: t.skipped}
		if !t.started.IsZero() {
			v := t.started
			st.LastStarted = &v
		}
		if !t.ended.IsZero() {
			v := t.ended
			st.LastFinished = &v
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Job != out[j].Job {
			return out[i].Job < out[j].Job
		}
		return out[i].Op < out[j].Op
	})
	return out
}
