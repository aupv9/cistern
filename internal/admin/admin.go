package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"cistern/internal/repo"
)

const Dir = "_admin"

type Actor struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name"`
}

var ActorScheduler = Actor{ID: "scheduler", Name: "Scheduler"}

func ActorCLI() Actor {
	a := Actor{ID: "cli", Name: "CLI"}
	host, _ := os.Hostname()
	if u := os.Getenv("USER"); u != "" && host != "" {
		a.Name = "CLI (" + u + "@" + host + ")"
	}
	return a
}

const (
	TriggerSchedule = "schedule"
	TriggerCLI      = "cli"
	TriggerUI       = "ui"
)

const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateUnknown   = "unknown"
)

const (
	OpBackup  = "backup"
	OpVerify  = "verify"
	OpRestore = "restore"
	OpPrune   = "prune"
	OpDrill   = "drill"
)

var idRe = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{8}$`)

func NewID(t time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func ValidID(id string) bool { return idRe.MatchString(id) }

func IDTime(id string) (time.Time, bool) {
	if len(id) < 16 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405Z", id[:16])
	return t, err == nil
}

type Record struct {
	ID          string        `json:"id"`
	Job         string        `json:"job"`
	Op          string        `json:"op"`
	Status      string        `json:"status"`
	Trigger     string        `json:"trigger"`
	RequestedBy *Actor        `json:"requested_by,omitempty"`
	ApprovalID  string        `json:"approval_id,omitempty"`
	Snapshot    string        `json:"snapshot,omitempty"`
	Target      string        `json:"target,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	FinishedAt  time.Time     `json:"finished_at"`
	DurationMS  int64         `json:"duration_ms"`
	StoredBytes int64         `json:"stored_bytes,omitempty"`
	Error       string        `json:"error,omitempty"`
	Drill       *DrillReport  `json:"drill,omitempty"`
	Prune       *PruneSummary `json:"prune,omitempty"`
}

type Run struct {
	ID          string        `json:"id"`
	Job         string        `json:"job"`
	Op          string        `json:"op"`
	State       string        `json:"state"`
	Trigger     string        `json:"trigger"`
	RequestedBy *Actor        `json:"requested_by,omitempty"`
	ApprovalID  string        `json:"approval_id,omitempty"`
	Snapshot    string        `json:"snapshot,omitempty"`
	Target      string        `json:"target,omitempty"`
	QueuedAt    *time.Time    `json:"queued_at,omitempty"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	FinishedAt  *time.Time    `json:"finished_at,omitempty"`
	DurationMS  *int64        `json:"duration_ms,omitempty"`
	StoredBytes *int64        `json:"stored_bytes,omitempty"`
	Error       string        `json:"error,omitempty"`
	Progress    *ProgressView `json:"progress,omitempty"`
	Drill       *DrillResult  `json:"drill,omitempty"`
	Prune       *PruneSummary `json:"prune,omitempty"`
	LogTail     []string      `json:"log_tail,omitempty"`
}

func (r *Record) Run() Run {
	started, finished, dur := r.StartedAt, r.FinishedAt, r.DurationMS
	out := Run{
		ID: r.ID, Job: r.Job, Op: r.Op, State: r.Status, Trigger: r.Trigger, RequestedBy: r.RequestedBy,
		ApprovalID: r.ApprovalID, Snapshot: r.Snapshot, Target: r.Target, Error: r.Error,
		StartedAt: &started, FinishedAt: &finished, DurationMS: &dur,
	}
	if r.StoredBytes > 0 {
		v := r.StoredBytes
		out.StoredBytes = &v
	}
	if r.Op == OpDrill && r.Drill != nil {
		out.Drill = DrillResultOf(out, r.Drill)
	}
	if r.Op == OpPrune && r.Prune != nil {
		p := *r.Prune
		out.Prune = &p
	}
	return out
}

func RunsDir(prefix, job string) string { return path.Join(prefix, Dir, "runs", job) }

func RunKey(prefix, job, id string) string { return path.Join(RunsDir(prefix, job), id+".json") }

func WriteRecord(ctx context.Context, r repo.Repository, prefix string, rec *Record) error {
	if !ValidID(rec.ID) {
		return fmt.Errorf("invalid run id %q", rec.ID)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := RunKey(prefix, rec.Job, rec.ID)
	if rt, ok := r.(repo.Retainer); ok && rt.LockMode() != "" && rt.LockDays() > 0 {
		return rt.PutIfAbsentUntil(ctx, key, append(data, '\n'), time.Now().Add(time.Duration(rt.LockDays())*24*time.Hour))
	}
	return r.PutIfAbsent(ctx, key, append(data, '\n'))
}

func RecordKeys(ctx context.Context, r repo.Repository, prefix, job string) ([]string, error) {
	dir := RunsDir(prefix, job) + "/"
	keys, err := r.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := keys[:0]
	for _, k := range keys {
		name := strings.TrimPrefix(k, dir)
		if id, ok := strings.CutSuffix(name, ".json"); ok && ValidID(id) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func ReadJSON(ctx context.Context, r repo.Repository, key string, v any) error {
	rc, err := r.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func ListRecords(ctx context.Context, r repo.Repository, prefix, job string) ([]*Record, error) {
	keys, err := RecordKeys(ctx, r, prefix, job)
	if err != nil {
		return nil, err
	}
	var out []*Record
	var errs []error
	for _, k := range keys {
		var rec Record
		if err := ReadJSON(ctx, r, k, &rec); err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, &rec)
	}
	return out, errors.Join(errs...)
}

type Progress struct {
	ArtifactsDone  atomic.Int64
	ArtifactsTotal atomic.Int64
	BytesDone      atomic.Int64
	BytesTotal     atomic.Int64
}

type ProgressView struct {
	ArtifactsDone  int64 `json:"artifacts_done"`
	ArtifactsTotal int64 `json:"artifacts_total,omitempty"`
	BytesDone      int64 `json:"bytes_done"`
	BytesTotal     int64 `json:"bytes_total,omitempty"`
}

func (p *Progress) View() *ProgressView {
	if p == nil {
		return nil
	}
	return &ProgressView{
		ArtifactsDone: p.ArtifactsDone.Load(), ArtifactsTotal: p.ArtifactsTotal.Load(),
		BytesDone: p.BytesDone.Load(), BytesTotal: p.BytesTotal.Load(),
	}
}

func (p *Progress) AddArtifact() {
	if p != nil {
		p.ArtifactsDone.Add(1)
	}
}

func (p *Progress) AddBytes(n int64) {
	if p != nil && n > 0 {
		p.BytesDone.Add(n)
	}
}

func (p *Progress) SetTotal(artifacts, bytes int64) {
	if p != nil {
		p.ArtifactsTotal.Store(artifacts)
		p.BytesTotal.Store(bytes)
	}
}

func IsSecretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "passwd", "secret", "token", "key", "credential", "passphrase", "auth", "cert", "private"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
