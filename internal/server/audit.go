package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"cistern/internal/admin"
	"cistern/internal/repo"
)

const (
	ActionLogin           = "auth.login"
	ActionAuthDenied      = "auth.denied"
	ActionLogout          = "auth.logout"
	ActionBackup          = "backup.run"
	ActionVerify          = "verify.run"
	ActionDrill           = "drill.run"
	ActionPrune           = "prune.run"
	ActionRestore         = "restore.run"
	ActionRestoreRequest  = "restore.request"
	ActionApprove         = "approval.approve"
	ActionReject          = "approval.reject"
	ActionApprovalExpired = "approval.expire"
	ActionRunFinish       = "run.finish"
	ActionAccessDenied    = "access.denied"
	ActionConfigView      = "config.view"
)

var auditActions = map[string]bool{
	ActionLogin: true, ActionAuthDenied: true, ActionLogout: true, ActionBackup: true, ActionVerify: true, ActionPrune: true,
	ActionRestore: true, ActionRestoreRequest: true, ActionApprove: true, ActionReject: true,
	ActionApprovalExpired: true, ActionRunFinish: true, ActionAccessDenied: true, ActionDrill: true, ActionConfigView: true,
}

const (
	ResultAccepted  = "accepted"
	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
	ResultDenied    = "denied"
	ResultPending   = "pending"
)

var auditResults = map[string]bool{ResultAccepted: true, ResultSucceeded: true, ResultFailed: true, ResultDenied: true, ResultPending: true}

type AuditEntry struct {
	ID         string            `json:"id"`
	Time       time.Time         `json:"time"`
	Actor      admin.Actor       `json:"actor"`
	Roles      []string          `json:"roles,omitempty"`
	Action     string            `json:"action"`
	Result     string            `json:"result"`
	Job        string            `json:"job,omitempty"`
	Snapshot   string            `json:"snapshot,omitempty"`
	Target     string            `json:"target,omitempty"`
	RunID      string            `json:"run_id,omitempty"`
	ApprovalID string            `json:"approval_id,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	Error      string            `json:"error,omitempty"`
	SourceIP   string            `json:"source_ip,omitempty"`
	Summary    string            `json:"summary,omitempty"`
}

var actorSystem = admin.Actor{ID: "system", Name: "Cistern"}

const auditIDLayout = "20060102T150405.000000000Z"

func newAuditID(t time.Time) string {
	var b [4]byte
	rand.Read(b[:])
	return t.UTC().Format(auditIDLayout) + "-" + hex.EncodeToString(b[:])
}

func auditIDTime(id string) (time.Time, bool) {
	if len(id) < len(auditIDLayout) {
		return time.Time{}, false
	}
	t, err := time.Parse(auditIDLayout, id[:len(auditIDLayout)])
	return t, err == nil
}

type dayListing struct {
	keys    []string
	fetched time.Time
}

type auditStore struct {
	r         repo.Repository
	prefix    string
	retain    time.Duration
	out       io.Writer
	now       func() time.Time
	outMu     sync.Mutex
	mu        sync.Mutex
	days      map[string]*dayListing
	entries   map[string]*AuditEntry
	maxCached int
}

func newAuditStore(r repo.Repository, prefix string, retainDays int, out io.Writer) *auditStore {
	return &auditStore{
		r: r, prefix: prefix, retain: time.Duration(retainDays) * 24 * time.Hour, out: out, now: time.Now,
		days: map[string]*dayListing{}, entries: map[string]*AuditEntry{}, maxCached: 50000,
	}
}

func (a *auditStore) dayDir(t time.Time) string {
	t = t.UTC()
	return path.Join(a.prefix, admin.Dir, "audit", t.Format("2006"), t.Format("01"), t.Format("02"))
}

func (a *auditStore) key(id string) (string, bool) {
	t, ok := auditIDTime(id)
	if !ok {
		return "", false
	}
	return path.Join(a.dayDir(t), id+".json"), true
}

func (a *auditStore) write(ctx context.Context, e *AuditEntry) error {
	now := a.now().UTC()
	e.Time = now
	e.ID = newAuditID(now)
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	key, _ := a.key(e.ID)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if rt, ok := a.r.(repo.Retainer); ok && rt.LockMode() != "" {
		err = rt.PutIfAbsentUntil(wctx, key, append(data, '\n'), now.Add(a.retain))
	} else {
		err = a.r.PutIfAbsent(wctx, key, append(data, '\n'))
	}
	a.emit(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cacheEntry(key, e)
	if d := a.days[a.dayDir(now)]; d != nil {
		d.keys = append(d.keys, key)
		sort.Strings(d.keys)
	}
	a.mu.Unlock()
	return nil
}

func (a *auditStore) emit(e *AuditEntry) {
	if a.out == nil {
		return
	}
	line, err := json.Marshal(struct {
		Kind string `json:"kind"`
		*AuditEntry
	}{"cistern.audit", e})
	if err != nil {
		return
	}
	a.outMu.Lock()
	a.out.Write(append(line, '\n'))
	a.outMu.Unlock()
}

func (a *auditStore) cacheEntry(key string, e *AuditEntry) {
	if len(a.entries) >= a.maxCached {
		a.entries = map[string]*AuditEntry{}
	}
	a.entries[key] = e
}

func (a *auditStore) dayKeys(ctx context.Context, day time.Time) ([]string, error) {
	dir := a.dayDir(day)
	now := a.now().UTC()
	ttl := 10 * time.Minute
	if dir == a.dayDir(now) || dir == a.dayDir(now.Add(-time.Hour)) {
		ttl = 3 * time.Second
	}
	a.mu.Lock()
	d := a.days[dir]
	a.mu.Unlock()
	if d != nil && now.Sub(d.fetched) < ttl {
		return d.keys, nil
	}
	keys, err := a.r.List(ctx, dir+"/")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		if strings.HasSuffix(k, ".json") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	a.mu.Lock()
	a.days[dir] = &dayListing{keys: out, fetched: now}
	a.mu.Unlock()
	return out, nil
}

func (a *auditStore) load(ctx context.Context, key string) (*AuditEntry, error) {
	a.mu.Lock()
	e := a.entries[key]
	a.mu.Unlock()
	if e != nil {
		return e, nil
	}
	var v AuditEntry
	if err := admin.ReadJSON(ctx, a.r, key, &v); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cacheEntry(key, &v)
	a.mu.Unlock()
	return &v, nil
}

type auditQuery struct {
	From, To time.Time
	Action   string
	Job      string
	Result   string
	Q        string
	Limit    int
	Cursor   string
	Exclude  map[string]bool
}

func (q *auditQuery) match(e *AuditEntry) bool {
	if e.Time.Before(q.From) || e.Time.After(q.To) {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if q.Exclude[e.Action] {
		return false
	}
	if q.Job != "" && e.Job != q.Job {
		return false
	}
	if q.Result != "" && e.Result != q.Result {
		return false
	}
	if q.Q != "" {
		needle := strings.ToLower(q.Q)
		hay := strings.ToLower(strings.Join([]string{e.Actor.Email, e.Actor.Name, e.Job, e.Snapshot}, "\x00"))
		if !strings.Contains(hay, needle) {
			return false
		}
	}
	return true
}

var errBadCursor = errors.New("invalid cursor")

func (a *auditStore) query(ctx context.Context, q auditQuery, visit func(*AuditEntry) bool) error {
	end := q.To
	if q.Cursor != "" {
		ct, ok := auditIDTime(q.Cursor)
		if !ok {
			return errBadCursor
		}
		if ct.Before(end) {
			end = ct
		}
	}
	startDay := time.Date(q.From.Year(), q.From.Month(), q.From.Day(), 0, 0, 0, 0, time.UTC)
	for day := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC); !day.Before(startDay); day = day.AddDate(0, 0, -1) {
		keys, err := a.dayKeys(ctx, day)
		if err != nil {
			return err
		}
		for i := len(keys) - 1; i >= 0; i-- {
			id := strings.TrimSuffix(path.Base(keys[i]), ".json")
			if q.Cursor != "" && id >= q.Cursor {
				continue
			}
			if t, ok := auditIDTime(id); ok && (t.Before(q.From) || t.After(q.To)) {
				continue
			}
			e, err := a.load(ctx, keys[i])
			if err != nil {
				continue
			}
			if e.ID == "" {
				e.ID = id
			}
			if !q.match(e) {
				continue
			}
			if !visit(e) {
				return nil
			}
		}
	}
	return nil
}

const totalCap = 10000

func (a *auditStore) page(ctx context.Context, q auditQuery) ([]*AuditEntry, string, error) {
	out, next, _, err := a.pageTotal(ctx, q, false)
	return out, next, err
}

func (a *auditStore) pageTotal(ctx context.Context, q auditQuery, count bool) ([]*AuditEntry, string, int, error) {
	out := []*AuditEntry{}
	next := ""
	total := 0
	err := a.query(ctx, q, func(e *AuditEntry) bool {
		total++
		if len(out) == q.Limit {
			if next == "" {
				next = out[len(out)-1].ID
			}
			return count && total <= totalCap
		}
		out = append(out, e)
		return true
	})
	if !count || total > totalCap {
		total = -1
	}
	return out, next, total, err
}
