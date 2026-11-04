package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/manifest"
	"cistern/internal/repo"
	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func serverSettings(lines ...string) func(string) string {
	return func(src string) string {
		return strings.Replace(src, "  name: test\n", "  name: test\n  "+strings.Join(lines, "\n  ")+"\n", 1)
	}
}

func expect429(t *testing.T, code int, hdr http.Header, body []byte) {
	t.Helper()
	var e apiErr
	json.Unmarshal(body, &e)
	secs, err := strconv.Atoi(hdr.Get("Retry-After"))
	if code != http.StatusTooManyRequests || e.Error.Code != codeRateLimited || err != nil || secs < 1 || secs > 60 {
		t.Fatalf("want 429 rate_limited with Retry-After: %d %q %v", code, hdr.Get("Retry-After"), string(body))
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t, noDaemon, nil, serverSettings("rate_limit: { login_per_minute: 3, write_per_minute: 2, read_per_minute: 4 }"))
	a := e.login("alice@example.com", "operator")
	b := e.login("bob@example.com", "operator")
	e.login("carol@example.com", "viewer")
	resp, body := e.raw("GET", "/api/v1/auth/dev-login?email=dave@example.com", nil, nil)
	expect429(t, resp.StatusCode, resp.Header, body)
	for _, p := range []string{"/api/v1/auth/login", "/api/v1/auth/callback?state=x&code=y"} {
		if resp, body := e.raw("GET", p, nil, nil); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s not limited: %d %s", p, resp.StatusCode, body)
		}
	}

	for i := 0; i < 2; i++ {
		a.expectError("POST", "/api/v1/jobs/app/backup", nil, 503, codeDaemon)
	}
	resp, body = e.raw("POST", "/api/v1/jobs/app/backup", nil, map[string]string{"Cookie": "cistern_session=" + a.cookie, "X-Cistern-CSRF": a.csrf})
	expect429(t, resp.StatusCode, resp.Header, body)
	b.expectError("POST", "/api/v1/jobs/app/backup", nil, 503, codeDaemon)

	for i := 0; i < 4; i++ {
		b.json("GET", "/api/v1/me", nil, 200, nil)
	}
	code, data := b.do("GET", "/api/v1/me", nil)
	if code != http.StatusTooManyRequests || !strings.Contains(string(data), codeRateLimited) {
		t.Fatalf("read limit: %d %s", code, data)
	}
	a.json("GET", "/api/v1/me", nil, 200, nil)
	if len(e.auditEntries(ActionBackup)) != 0 {
		t.Fatal("a rate-limited request reached the handler")
	}
}

func TestRateLimitsOffAndTrustedProxies(t *testing.T) {
	e := newEnv(t, noDaemon, nil, serverSettings("rate_limit: { login_per_minute: 0, write_per_minute: 0 }"))
	u := e.login("alice@example.com", "operator")
	for i := 0; i < 15; i++ {
		e.login("alice@example.com", "operator")
		u.expectError("POST", "/api/v1/jobs/app/backup", nil, 503, codeDaemon)
	}

	p := newEnv(t, noDaemon, nil, serverSettings("rate_limit: { login_per_minute: 1 }", `trusted_proxies: ["127.0.0.0/8", "::1"]`))
	login := func(ip string) int {
		resp, _ := p.raw("GET", "/api/v1/auth/dev-login?email=x@example.com", nil, map[string]string{"X-Forwarded-For": "6.6.6.6, " + ip})
		return resp.StatusCode
	}
	if login("198.51.100.1") != 200 || login("198.51.100.1") != 429 || login("198.51.100.2") != 200 {
		t.Fatal("login limit is not per forwarded client")
	}
	logins := p.auditEntries(ActionLogin)
	if len(logins) != 2 || logins[1].SourceIP != "198.51.100.2" {
		t.Fatalf("source ip: %+v", logins)
	}

	q := newEnv(t, noDaemon, nil, serverSettings("rate_limit: { login_per_minute: 1 }"))
	resp, _ := q.raw("GET", "/api/v1/auth/dev-login", nil, map[string]string{"X-Forwarded-For": "198.51.100.1"})
	resp2, _ := q.raw("GET", "/api/v1/auth/dev-login", nil, map[string]string{"X-Forwarded-For": "198.51.100.2"})
	if resp.StatusCode != 200 || resp2.StatusCode != 429 {
		t.Fatalf("X-Forwarded-For trusted without trusted_proxies: %d %d", resp.StatusCode, resp2.StatusCode)
	}
	if ip := q.auditEntries(ActionLogin)[0].SourceIP; ip != "127.0.0.1" {
		t.Fatalf("source ip without trusted proxies: %s", ip)
	}
}

func (e *env) newReplica() (*Server, *httptest.Server) {
	e.t.Helper()
	scfg, err := config.LoadFor(filepath.Join(e.dir, "cistern.yaml"), config.ProcessServer)
	if err != nil {
		e.t.Fatal(err)
	}
	srv, err := New(Options{Config: scfg, Version: "test", DevLogin: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AuditOut: io.Discard})
	if err != nil {
		e.t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	e.t.Cleanup(ts.Close)
	return srv, ts
}

func getMe(t *testing.T, base, cookie string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/v1/me", nil)
	req.Header.Set("Cookie", "cistern_session="+cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestSessionRevocationPersists(t *testing.T) {
	e := newEnv(t, noDaemon, nil)
	u := e.login("alice@example.com", "viewer")
	other := e.login("bob@example.com", "viewer")
	old := u.cookie
	replica, rts := e.newReplica()
	if getMe(t, rts.URL, old) != 200 {
		t.Fatal("a replica with the same session key must accept the session")
	}

	u.json("POST", "/api/v1/auth/logout", nil, 204, nil)
	if getMe(t, e.ts.URL, old) != 401 {
		t.Fatal("logged-out session accepted")
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "repo", "_admin", "sessions", "revoked", "*.json"))
	if len(files) != 1 {
		t.Fatalf("revocation objects: %v", files)
	}
	var rec revocationRecord
	data, _ := os.ReadFile(files[0])
	if json.Unmarshal(data, &rec) != nil || rec.Subject != "dev:alice@example.com" || rec.SID == "" ||
		filepath.Base(files[0]) != rec.SID+".json" || !rec.ExpiresAt.After(time.Now().Add(7*time.Hour)) || rec.RevokedAt.IsZero() {
		t.Fatalf("revocation: %s", data)
	}
	if len(e.auditEntries(ActionLogout)) != 1 {
		t.Fatal("logout not audited")
	}

	if err := replica.revoked.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if getMe(t, rts.URL, old) != 401 {
		t.Fatal("the replica accepts a session revoked on another replica after a refresh")
	}

	_, restarted := e.newReplica()
	if getMe(t, restarted.URL, old) != 401 {
		t.Fatal("a restarted server accepts a logged-out session")
	}
	if getMe(t, restarted.URL, other.cookie) != 200 {
		t.Fatal("a restarted server rejects a valid session")
	}
}

func TestExpiredRevocationsAreDeleted(t *testing.T) {
	e := newEnv(t, noDaemon, nil)
	dir := filepath.Join(e.dir, "repo", "_admin", "sessions", "revoked")
	os.MkdirAll(dir, 0o755)
	write := func(sid string, exp time.Time) string {
		data, _ := json.Marshal(revocationRecord{SID: sid, Subject: "s", RevokedAt: exp.Add(-time.Hour), ExpiresAt: exp})
		p := filepath.Join(dir, sid+".json")
		os.WriteFile(p, data, 0o644)
		return p
	}
	expired := write("expiredsession0000000000", time.Now().Add(-time.Minute))
	live := write("livesession0000000000000", time.Now().Add(time.Hour))
	if err := e.srv.revoked.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatal("expired revocation not deleted")
	}
	if _, err := os.Stat(live); err != nil || !e.srv.revoked.has("livesession0000000000000") || e.srv.revoked.has("expiredsession0000000000") {
		t.Fatal("live revocation not loaded")
	}
}

func newLockedS3(t *testing.T) (*repo.S3, *s3test.Server) {
	t.Helper()
	srv := s3test.New(true)
	t.Cleanup(srv.Close)
	r, err := repo.NewS3(repo.S3Config{
		Endpoint: srv.URL, Region: s3test.Region, Bucket: s3test.Bucket, PathStyle: true,
		AccessKeyID: s3test.AccessKey, SecretAccessKey: s3test.SecretKey, PartSizeMB: 5,
		LockMode: s3.LockCompliance, LockDays: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, srv
}

func TestObjectLockOnApprovalsRunsAndRevocations(t *testing.T) {
	ctx := context.Background()
	r, srv := newLockedS3(t)
	day := 24 * time.Hour
	locked := func(key string, days int) {
		t.Helper()
		v, ok := srv.Current(key)
		want := time.Now().Add(time.Duration(days) * day)
		if !ok || v.LockMode != s3.LockCompliance || v.RetainUntil.Before(want.Add(-time.Hour)) || v.RetainUntil.After(want.Add(time.Hour)) {
			t.Fatalf("%s: want %s until about %v, got %+v", key, s3.LockCompliance, want, v)
		}
	}

	st := newApprovalStore(r, "p", 400)
	id := admin.NewID(time.Now())
	if err := st.create(ctx, &approvalRequest{ID: id, Job: "app", Snapshot: "s", Target: "prod", Reason: "r", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.decide(ctx, id, &approvalDecision{State: ApprovalApproved, DecidedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.decide(ctx, id, &approvalDecision{State: ApprovalRejected, DecidedAt: time.Now()}); err != errDecided {
		t.Fatalf("second decision: %v", err)
	}
	if err := st.setRun(ctx, id, &approvalRun{RunID: admin.NewID(time.Now())}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"request.json", "decision.json", "run.json"} {
		locked("p/_admin/approvals/"+id+"/"+name, 400)
	}

	rec := &admin.Record{ID: admin.NewID(time.Now()), Job: "app", Op: admin.OpBackup, Status: admin.StateSucceeded}
	if err := admin.WriteRecord(ctx, r, "p", rec); err != nil {
		t.Fatal(err)
	}
	locked(admin.RunKey("p", "app", rec.ID), 7)

	rv := newRevocations(r, "p", slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	sess := &session{ID: randomToken(16), Subject: "u", Expires: time.Now().Add(time.Hour).Unix()}
	if err := rv.revoke(ctx, sess); err != nil {
		t.Fatal(err)
	}
	v, ok := srv.Current("p/_admin/sessions/revoked/" + sess.ID + ".json")
	if !ok || v.LockMode != "" {
		t.Fatalf("session revocations must never be locked: %+v", v)
	}

	plain := t.TempDir()
	lr, _ := repo.NewLocal(plain)
	if err := newApprovalStore(lr, "", 400).create(ctx, &approvalRequest{ID: id, Job: "app", RequestedAt: time.Now()}); err != nil {
		t.Fatalf("local repository: %v", err)
	}
}

func TestEncryptionViewFromManifests(t *testing.T) {
	s := &Server{cfg: &config.Config{Encryption: config.Encryption{Recipients: []string{"age1a", "", "age1b"}}}}
	if v := s.encryptionOf(&jobData{}); v.Mode != "age-x25519" || v.Recipients != 2 {
		t.Fatalf("from config: %+v", v)
	}
	old := &manifest.Manifest{FinishedAt: time.Unix(1, 0), Recipients: []string{"a", "b", "c"}}
	newest := &manifest.Manifest{FinishedAt: time.Unix(2, 0)}
	if v := s.encryptionOf(&jobData{snaps: []*manifest.Manifest{newest, old}}); v.Mode != "none" || v.Recipients != 0 {
		t.Fatalf("newest snapshot unencrypted: %+v", v)
	}
	newest.Recipients = []string{"a"}
	if v := s.encryptionOf(&jobData{snaps: []*manifest.Manifest{old, newest}}); v.Mode != "age-x25519" || v.Recipients != 1 {
		t.Fatalf("newest snapshot: %+v", v)
	}
	s.cfg.Encryption = config.Encryption{RecipientsFile: "r.txt"}
	if v := s.encryptionOf(&jobData{}); v.Mode != "age-x25519" {
		t.Fatalf("recipients_file only: %+v", v)
	}
}
