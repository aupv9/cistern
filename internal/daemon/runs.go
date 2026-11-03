package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"cistern/internal/admin"
)

type RunRequest struct {
	RunID         string            `json:"run_id,omitempty"`
	Op            string            `json:"op"`
	Job           string            `json:"job"`
	Snapshot      string            `json:"snapshot,omitempty"`
	Target        string            `json:"target,omitempty"`
	Overrides     map[string]string `json:"overrides,omitempty"`
	RestoreSuffix *string           `json:"restore_suffix,omitempty"`
	RequestedBy   *admin.Actor      `json:"requested_by,omitempty"`
	ApprovalID    string            `json:"approval_id,omitempty"`

	Trigger  string          `json:"-"`
	Log      *slog.Logger    `json:"-"`
	Progress *admin.Progress `json:"-"`
}

type Result struct {
	Snapshot    string
	StoredBytes int64
}

type Executor func(ctx context.Context, req *RunRequest) (Result, error)

type InvalidError struct {
	Message string
	Fields  map[string]string
}

func (e *InvalidError) Error() string { return e.Message }

var (
	ErrBusy     = errors.New("the job already has a running operation")
	ErrClosed   = errors.New("the daemon is shutting down")
	ErrNotFound = errors.New("not found")
)

const (
	logTailLines = 200
	keepFinished = 500
	keepFor      = 24 * time.Hour
)

type runEntry struct {
	mu       sync.Mutex
	run      admin.Run
	progress *admin.Progress
	logs     *lineRing
}

func (e *runEntry) view(withLogs bool) admin.Run {
	e.mu.Lock()
	r := e.run
	e.mu.Unlock()
	if r.Progress == nil {
		r.Progress = e.progress.View()
	}
	if withLogs {
		r.LogTail = e.logs.lines()
	}
	return r
}

func (e *runEntry) finishedAt() *time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.run.FinishedAt
}

type registry struct {
	exec Executor
	log  *slog.Logger
	now  func() time.Time

	mu     sync.Mutex
	runs   map[string]*runEntry
	busy   map[string]string
	closed bool
	wg     sync.WaitGroup
}

func newRegistry(exec Executor, log *slog.Logger) *registry {
	return &registry{exec: exec, log: log, now: time.Now, runs: map[string]*runEntry{}, busy: map[string]string{}}
}

func (g *registry) isBusy(job string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.busy[job]
	return ok
}

func (g *registry) begin(req *RunRequest) (*runEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, ErrClosed
	}
	if id, ok := g.busy[req.Job]; ok {
		op := ""
		if e := g.runs[id]; e != nil {
			op = e.view(false).Op
		}
		return nil, fmt.Errorf("%w (%s, run %s)", ErrBusy, op, id)
	}
	now := g.now().UTC()
	if req.RunID == "" {
		req.RunID = admin.NewID(now)
	}
	if _, dup := g.runs[req.RunID]; dup {
		return nil, &InvalidError{Message: "run_id already used", Fields: map[string]string{"run_id": "already used"}}
	}
	g.gc(now)
	e := &runEntry{progress: &admin.Progress{}, logs: newLineRing(logTailLines)}
	e.run = admin.Run{
		ID: req.RunID, Job: req.Job, Op: req.Op, State: admin.StateQueued, Trigger: req.Trigger,
		RequestedBy: req.RequestedBy, ApprovalID: req.ApprovalID, Snapshot: req.Snapshot, Target: req.Target,
		QueuedAt: &now,
	}
	if e.run.Snapshot == "" && (req.Op == admin.OpVerify || req.Op == admin.OpRestore) {
		e.run.Snapshot = "latest"
	}
	g.runs[req.RunID] = e
	g.busy[req.Job] = req.RunID
	g.wg.Add(1)
	return e, nil
}

func (g *registry) gc(now time.Time) {
	type done struct {
		id string
		at time.Time
	}
	var finished []done
	for id, e := range g.runs {
		if at := e.finishedAt(); at != nil {
			finished = append(finished, done{id, *at})
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].at.After(finished[j].at) })
	for i, d := range finished {
		if i >= keepFinished || now.Sub(d.at) > keepFor {
			delete(g.runs, d.id)
		}
	}
}

func (g *registry) execute(ctx context.Context, e *runEntry, req *RunRequest) error {
	defer g.wg.Done()
	started := g.now().UTC()
	e.mu.Lock()
	e.run.State, e.run.StartedAt = admin.StateRunning, &started
	e.mu.Unlock()

	base := g.log
	if base == nil {
		base = slog.Default()
	}
	req.Log = slog.New(teeHandler{base.Handler(), slog.NewTextHandler(e.logs, nil)}).With("run", req.RunID)
	req.Progress = e.progress

	res, err := g.run(ctx, req)

	finished := g.now().UTC()
	dur := finished.Sub(started).Milliseconds()
	e.mu.Lock()
	e.run.FinishedAt, e.run.DurationMS = &finished, &dur
	e.run.Progress = e.progress.View()
	if res.Snapshot != "" {
		e.run.Snapshot = res.Snapshot
	}
	if res.StoredBytes > 0 {
		v := res.StoredBytes
		e.run.StoredBytes = &v
	}
	if err != nil {
		e.run.State, e.run.Error = admin.StateFailed, err.Error()
	} else {
		e.run.State = admin.StateSucceeded
	}
	e.mu.Unlock()

	g.mu.Lock()
	if g.busy[req.Job] == req.RunID {
		delete(g.busy, req.Job)
	}
	g.mu.Unlock()
	return err
}

func (g *registry) run(ctx context.Context, req *RunRequest) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("internal error: %v", p)
			g.log.Error("run panicked", "run", req.RunID, "job", req.Job, "op", req.Op, "panic", p)
		}
	}()
	if g.exec == nil {
		return Result{}, errors.New("no executor configured")
	}
	return g.exec(ctx, req)
}

func (g *registry) get(id string) (admin.Run, bool) {
	g.mu.Lock()
	e := g.runs[id]
	g.mu.Unlock()
	if e == nil {
		return admin.Run{}, false
	}
	return e.view(true), true
}

func (g *registry) list(active bool) []admin.Run {
	g.mu.Lock()
	entries := make([]*runEntry, 0, len(g.runs))
	for _, e := range g.runs {
		entries = append(entries, e)
	}
	g.mu.Unlock()
	out := []admin.Run{}
	for _, e := range entries {
		r := e.view(false)
		if active && r.State != admin.StateQueued && r.State != admin.StateRunning {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

type RunningOp struct {
	Job       string     `json:"job"`
	Op        string     `json:"op"`
	RunID     string     `json:"run_id"`
	Trigger   string     `json:"trigger"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

func (g *registry) running() []RunningOp {
	out := []RunningOp{}
	for _, r := range g.list(true) {
		started := r.StartedAt
		if started == nil {
			started = r.QueuedAt
		}
		out = append(out, RunningOp{Job: r.Job, Op: r.Op, RunID: r.ID, Trigger: r.Trigger, StartedAt: started})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}

func (g *registry) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

type lineRing struct {
	mu    sync.Mutex
	max   int
	buf   []string
	start int
	part  strings.Builder
}

func newLineRing(max int) *lineRing { return &lineRing{max: max} }

func (r *lineRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := string(p)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			r.part.WriteString(s)
			break
		}
		r.part.WriteString(s[:i])
		r.push(r.part.String())
		r.part.Reset()
		s = s[i+1:]
	}
	return len(p), nil
}

func (r *lineRing) push(line string) {
	if len(line) > 4096 {
		line = line[:4096] + "…"
	}
	if len(r.buf) < r.max {
		r.buf = append(r.buf, line)
		return
	}
	r.buf[r.start] = line
	r.start = (r.start + 1) % r.max
}

func (r *lineRing) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.buf))
	out = append(out, r.buf[r.start:]...)
	out = append(out, r.buf[:r.start]...)
	return out
}

type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	var errs []error
	for _, h := range t {
		if h.Enabled(ctx, rec.Level) {
			errs = append(errs, h.Handle(ctx, rec.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}
