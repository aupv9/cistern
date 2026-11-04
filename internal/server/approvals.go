package server

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"cistern/internal/admin"
	"cistern/internal/repo"
)

const approvalTTL = 24 * time.Hour

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
	ApprovalExpired  = "expired"
)

type approvalRequest struct {
	ID            string      `json:"id"`
	Job           string      `json:"job"`
	Snapshot      string      `json:"snapshot"`
	Target        string      `json:"target"`
	RestoreSuffix *string     `json:"restore_suffix,omitempty"`
	Reason        string      `json:"reason"`
	RequestedBy   admin.Actor `json:"requested_by"`
	RequestedAt   time.Time   `json:"requested_at"`
}

type approvalDecision struct {
	State     string       `json:"state"`
	DecidedBy *admin.Actor `json:"decided_by,omitempty"`
	DecidedAt time.Time    `json:"decided_at"`
	Note      string       `json:"note,omitempty"`
}

type approvalRun struct {
	RunID string `json:"run_id,omitempty"`
	Error string `json:"error,omitempty"`
}

type approvalRecord struct {
	Request  approvalRequest
	Decision *approvalDecision
	Run      *approvalRun
}

func (a *approvalRecord) state(now time.Time) string {
	if a.Decision != nil {
		return a.Decision.State
	}
	if now.After(a.expiresAt()) {
		return ApprovalExpired
	}
	return ApprovalPending
}

func (a *approvalRecord) expiresAt() time.Time { return a.Request.RequestedAt.Add(approvalTTL) }

type approvalStore struct {
	r      repo.Repository
	prefix string
	retain time.Duration
	now    func() time.Time

	mu      sync.Mutex
	objects map[string]json.RawMessage
	listed  []string
	fetched time.Time
}

func newApprovalStore(r repo.Repository, prefix string, retainDays int) *approvalStore {
	return &approvalStore{r: r, prefix: prefix, retain: time.Duration(retainDays) * 24 * time.Hour, now: time.Now, objects: map[string]json.RawMessage{}}
}

func (s *approvalStore) dir() string { return path.Join(s.prefix, admin.Dir, "approvals") }

func (s *approvalStore) key(id, name string) string { return path.Join(s.dir(), id, name) }

var errDecided = errors.New("the request was already decided")

func (s *approvalStore) put(ctx context.Context, id, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	key := s.key(id, name)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if rt, ok := s.r.(repo.Retainer); ok && rt.LockMode() != "" {
		err = rt.PutIfAbsentUntil(wctx, key, append(data, '\n'), s.now().Add(s.retain))
	} else {
		err = s.r.PutIfAbsent(wctx, key, append(data, '\n'))
	}
	if err != nil {
		if errors.Is(err, repo.ErrExist) {
			return errDecided
		}
		return err
	}
	s.mu.Lock()
	s.objects[key] = data
	s.listed = append(s.listed, key)
	s.mu.Unlock()
	return nil
}

func (s *approvalStore) create(ctx context.Context, req *approvalRequest) error {
	return s.put(ctx, req.ID, "request.json", req)
}

func (s *approvalStore) decide(ctx context.Context, id string, d *approvalDecision) error {
	return s.put(ctx, id, "decision.json", d)
}

func (s *approvalStore) setRun(ctx context.Context, id string, r *approvalRun) error {
	return s.put(ctx, id, "run.json", r)
}

func (s *approvalStore) keys(ctx context.Context, fresh bool) ([]string, error) {
	s.mu.Lock()
	if !fresh && time.Since(s.fetched) < 5*time.Second {
		out := append([]string(nil), s.listed...)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	keys, err := s.r.List(ctx, s.dir()+"/")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.listed, s.fetched = keys, time.Now()
	s.mu.Unlock()
	return keys, nil
}

func (s *approvalStore) object(ctx context.Context, key string, v any) error {
	s.mu.Lock()
	data, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		var raw json.RawMessage
		if err := admin.ReadJSON(ctx, s.r, key, &raw); err != nil {
			return err
		}
		data = raw
		s.mu.Lock()
		s.objects[key] = raw
		s.mu.Unlock()
	}
	return json.Unmarshal(data, v)
}

func (s *approvalStore) list(ctx context.Context, fresh bool) ([]*approvalRecord, error) {
	keys, err := s.keys(ctx, fresh)
	if err != nil {
		return nil, err
	}
	return s.build(ctx, keys), nil
}

func (s *approvalStore) build(ctx context.Context, keys []string) []*approvalRecord {
	byID := map[string]map[string]string{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, s.dir()+"/")
		id, name, ok := strings.Cut(rest, "/")
		if !ok || !admin.ValidID(id) {
			continue
		}
		if byID[id] == nil {
			byID[id] = map[string]string{}
		}
		byID[id][name] = k
	}
	var out []*approvalRecord
	for id, files := range byID {
		rk, ok := files["request.json"]
		if !ok {
			continue
		}
		rec := &approvalRecord{}
		if err := s.object(ctx, rk, &rec.Request); err != nil {
			continue
		}
		rec.Request.ID = id
		if dk, ok := files["decision.json"]; ok {
			var d approvalDecision
			if s.object(ctx, dk, &d) == nil {
				rec.Decision = &d
			}
		}
		if rk, ok := files["run.json"]; ok {
			var r approvalRun
			if s.object(ctx, rk, &r) == nil {
				rec.Run = &r
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Request.ID > out[j].Request.ID })
	return out
}

func (s *approvalStore) get(ctx context.Context, id string) (*approvalRecord, error) {
	if !admin.ValidID(id) {
		return nil, errNotFound
	}
	keys, err := s.r.List(ctx, path.Join(s.dir(), id)+"/")
	if err != nil {
		return nil, err
	}
	for _, a := range s.build(ctx, keys) {
		if a.Request.ID == id {
			return a, nil
		}
	}
	return nil, errNotFound
}

var errNotFound = errors.New("not found")
