package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"cistern/internal/admin"
	"cistern/internal/repo"
)

const revocationRefresh = 30 * time.Second

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

type revocationRecord struct {
	SID       string    `json:"sid"`
	Subject   string    `json:"subject"`
	RevokedAt time.Time `json:"revoked_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type revocations struct {
	r      repo.Repository
	prefix string
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	m       map[string]time.Time
	expired map[string]bool
}

func newRevocations(r repo.Repository, prefix string, log *slog.Logger, now func() time.Time) *revocations {
	return &revocations{r: r, prefix: prefix, log: log, now: now, m: map[string]time.Time{}, expired: map[string]bool{}}
}

func (v *revocations) dir() string { return path.Join(v.prefix, admin.Dir, "sessions", "revoked") }

func (v *revocations) key(sid string) string { return path.Join(v.dir(), sid+".json") }

func (v *revocations) has(sid string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.m[sid]
	return ok
}

func (v *revocations) remember(sid string, until time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[sid] = until
}

func (v *revocations) revoke(ctx context.Context, sess *session) error {
	now := v.now().UTC()
	rec := revocationRecord{SID: sess.ID, Subject: sess.Subject, RevokedAt: now, ExpiresAt: sess.expiresAt()}
	v.remember(sess.ID, rec.ExpiresAt)
	if !sessionIDRe.MatchString(sess.ID) {
		return errors.New("session id has an unexpected format")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := v.r.PutIfAbsent(wctx, v.key(sess.ID), append(data, '\n')); err != nil && !errors.Is(err, repo.ErrExist) {
		return err
	}
	return nil
}

func (v *revocations) load(ctx context.Context) error {
	dir := v.dir() + "/"
	keys, err := v.r.List(ctx, dir)
	if err != nil {
		return err
	}
	now := v.now()
	listed := map[string]bool{}
	for _, k := range keys {
		sid, ok := strings.CutSuffix(strings.TrimPrefix(k, dir), ".json")
		if !ok || !sessionIDRe.MatchString(sid) {
			continue
		}
		listed[sid] = true
		v.mu.Lock()
		until, known := v.m[sid]
		gone := v.expired[sid]
		v.mu.Unlock()
		if gone {
			continue
		}
		if !known {
			var rec revocationRecord
			if err := admin.ReadJSON(ctx, v.r, k, &rec); err != nil {
				if errors.Is(err, repo.ErrNotExist) {
					continue
				}
				return err
			}
			until = rec.ExpiresAt
		}
		if now.After(until) {
			v.mu.Lock()
			delete(v.m, sid)
			v.expired[sid] = true
			v.mu.Unlock()
			if err := v.r.Delete(ctx, k); err != nil {
				v.log.Debug("deleting an expired session revocation failed", "key", k, "err", err)
			}
			continue
		}
		v.remember(sid, until)
	}
	v.mu.Lock()
	for sid, until := range v.m {
		if now.After(until) && !listed[sid] {
			delete(v.m, sid)
		}
	}
	for sid := range v.expired {
		if !listed[sid] {
			delete(v.expired, sid)
		}
	}
	v.mu.Unlock()
	return nil
}

func (v *revocations) loop(ctx context.Context) {
	t := time.NewTicker(revocationRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := v.load(lctx); err != nil && ctx.Err() == nil {
				v.log.Warn("refreshing session revocations failed", "err", err)
			}
			cancel()
		}
	}
}
