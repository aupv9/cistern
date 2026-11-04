package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/driver"
	"cistern/internal/engine"
	"cistern/internal/runner"
)

type uiDriver struct{ p driver.Params }

var (
	restoredMu sync.Mutex
	restored   = map[string]driver.Params{}
)

func init() {
	driver.Register("uitest", func(p driver.Params) (driver.Driver, error) { return &uiDriver{p: p}, nil })
}

func (d *uiDriver) Name() string                { return "uitest" }
func (d *uiDriver) Check(context.Context) error { return nil }

func (d *uiDriver) Backup(ctx context.Context, s driver.Sink) (driver.Info, error) {
	if d.p["fail"] == "true" {
		return driver.Info{}, errors.New("dump crashed")
	}
	w, err := s.Create("db/app.dump")
	if err != nil {
		return driver.Info{}, err
	}
	w.Write(bytes.Repeat([]byte("row\n"), 1000))
	if err := w.Close(); err != nil {
		return driver.Info{}, err
	}
	return driver.Info{Version: "16.4", Host: d.p["host"]}, nil
}

func (d *uiDriver) Restore(ctx context.Context, src driver.Source) error {
	for _, n := range src.List() {
		rc, err := src.Open(n)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, rc)
		if err := rc.Close(); err != nil {
			return err
		}
	}
	restoredMu.Lock()
	restored[d.p["db_marker"]] = d.p
	restoredMu.Unlock()
	return nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var secrets = map[string]string{
	"PG_PASSWORD":      "pg-SECRET-pass-1",
	"S3_KEY_ID":        "AKIASECRETID3",
	"S3_SECRET":        "S3SECRETKEYvalue-2",
	"DAEMON_TOKEN":     "daemon-token-SECRET-4-xxxxxxxxxxxxxxxxxxxx",
	"OIDC_SECRET":      "oidc-client-SECRET-5",
	"SESSION_KEY":      "session-key-SECRET-6-xxxxxxxxxxxxxxxxxxxxxx",
	"STAGING_PASSWORD": "staging-SECRET-7",
	"WEBHOOK_URL":      "https://hooks.example.com/SECRET-8",
}

const testConfig = `
repositories:
  local: { type: local, path: repo }
  offsite:
    type: s3
    endpoint: https://s3.example.com
    bucket: b
    access_key_id: ${S3_KEY_ID}
    secret_access_key: ${S3_SECRET}
encryption:
  allow_plaintext: true
notify:
  webhooks: [ { url: "${WEBHOOK_URL}", format: json } ]
defaults:
  repository: local
  retention: { last: 5 }
daemon:
  api_token: ${DAEMON_TOKEN}
server:
  name: test
  listen: 127.0.0.1:0
  session_key: ${SESSION_KEY}
  oidc: { issuer: "https://idp.invalid", client_id: cistern, client_secret: "${OIDC_SECRET}" }
  roles: [ { group: ops, roles: [operator] } ]
  daemon: { url: "DAEMON_URL", token: "${DAEMON_TOKEN}" }
jobs:
  - name: app
    driver: uitest
    schedule: "0 2 * * *"
    verify_schedule: "@daily"
    params: { host: db.internal, port: "5432", user: backup, password: "${PG_PASSWORD}", db_marker: app-original }
    restore_targets:
      - { name: staging, description: "staging-db:5432", production: false, set: { host: staging-db, password: "${STAGING_PASSWORD}", db_marker: app-staging } }
      - { name: prod, description: "the real one", production: true, set: { db_marker: app-prod } }
  - name: broken
    driver: uitest
    params: { fail: "true", password: "${PG_PASSWORD}" }
  - name: idle
    driver: uitest
    params: {}
`

type env struct {
	t      *testing.T
	dir    string
	srv    *Server
	ts     *httptest.Server
	audit  *syncBuf
	logs   *syncBuf
	bodies *syncBuf
	stop   func()
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

const (
	withDaemon = "daemon"
	noDaemon   = "none"
	daemonDown = "down"
)

func newEnv(t *testing.T, mode string, ui fs.FS, edits ...func(string) string) *env {
	t.Helper()
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	daddr := freeAddr(t)
	src := strings.Replace(testConfig, "DAEMON_URL", "http://"+daddr, 1)
	if mode == noDaemon {
		src = strings.Replace(src, `  daemon: { url: "http://`+daddr+`", token: "${DAEMON_TOKEN}" }`+"\n", "", 1)
	}
	for _, ed := range edits {
		src = ed(src)
	}
	cfgPath := filepath.Join(dir, "cistern.yaml")
	os.WriteFile(cfgPath, []byte(src), 0o600)
	e := &env{t: t, dir: dir, audit: &syncBuf{}, logs: &syncBuf{}, bodies: &syncBuf{}}
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if mode == withDaemon {
		dcfg, err := config.LoadFor(cfgPath, config.ProcessDaemon)
		if err != nil {
			t.Fatal(err)
		}
		eng, err := engine.New(dcfg, "test", log)
		if err != nil {
			t.Fatal(err)
		}
		maddr := freeAddr(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			daemon.Run(ctx, daemon.Options{
				Listen: maddr, APIListen: daddr, Grace: 5 * time.Second, Log: log, Status: eng.Status, Location: time.UTC, APIToken: dcfg.Daemon.APIToken,
				Prepare: func(req *daemon.RunRequest) error { return runner.Prepare(dcfg, req) },
				Execute: func(ctx context.Context, req *daemon.RunRequest) (daemon.Result, error) {
					return runner.Execute(ctx, eng, req)
				},
			})
		}()
		for i := 0; i < 100; i++ {
			if c, err := net.Dial("tcp", daddr); err == nil {
				c.Close()
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	scfg, err := config.LoadFor(cfgPath, config.ProcessServer)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Config: scfg, Version: "test", DevLogin: true, Log: log, AuditOut: e.audit, UI: ui})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.ts = httptest.NewServer(srv.Handler())
	wg.Add(1)
	go func() { defer wg.Done(); srv.tracker.loop(ctx) }()
	e.stop = func() { e.ts.Close(); cancel(); wg.Wait() }
	t.Cleanup(e.stop)
	return e
}

type user struct {
	e      *env
	cookie string
	csrf   string
	me     meResponse
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *env) raw(method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	e.bodies.Write(data)
	for _, vals := range resp.Header {
		for _, v := range vals {
			e.bodies.Write([]byte(v + "\n"))
		}
	}
	return resp, data
}

func (e *env) login(email, roles string) *user {
	e.t.Helper()
	resp, data := e.raw("GET", "/api/v1/auth/dev-login?email="+email+"&roles="+roles, nil, nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("dev login: %d %s", resp.StatusCode, data)
	}
	u := &user{e: e}
	for _, c := range resp.Cookies() {
		if c.Name == "cistern_session" {
			u.cookie = c.Value
		}
	}
	json.Unmarshal(data, &u.me)
	u.csrf = u.me.CSRFToken
	if u.cookie == "" || u.csrf == "" {
		e.t.Fatalf("no session: %s", data)
	}
	return u
}

func (u *user) do(method, path string, body any) (int, []byte) {
	u.e.t.Helper()
	hdr := map[string]string{"Cookie": "cistern_session=" + u.cookie}
	var rd io.Reader
	if method != "GET" {
		hdr["X-Cistern-CSRF"] = u.csrf
	}
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	resp, data := u.e.raw(method, path, rd, hdr)
	for _, c := range resp.Cookies() {
		if c.Name == "cistern_session" && c.Value != "" {
			u.cookie = c.Value
		}
	}
	return resp.StatusCode, data
}

func (u *user) json(method, path string, body any, want int, out any) {
	u.e.t.Helper()
	code, data := u.do(method, path, body)
	if code != want {
		u.e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			u.e.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

func (u *user) wait(id string) admin.Run {
	u.e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var r admin.Run
		u.json("GET", "/api/v1/runs/"+id, nil, 200, &r)
		if r.State == admin.StateSucceeded || r.State == admin.StateFailed {
			return r
		}
		time.Sleep(30 * time.Millisecond)
	}
	u.e.t.Fatalf("run %s did not finish", id)
	return admin.Run{}
}

type apiErr struct {
	Error errorBody `json:"error"`
}

func (u *user) expectError(method, path string, body any, status int, code string) apiErr {
	u.e.t.Helper()
	c, data := u.do(method, path, body)
	var e apiErr
	json.Unmarshal(data, &e)
	if c != status || e.Error.Code != code {
		u.e.t.Fatalf("%s %s: got %d %q, want %d %q: %s", method, path, c, e.Error.Code, status, code, data)
	}
	return e
}

func (e *env) auditEntries(action string) []AuditEntry {
	var out []AuditEntry
	for _, line := range strings.Split(e.audit.String(), "\n") {
		if line == "" {
			continue
		}
		var a AuditEntry
		json.Unmarshal([]byte(line), &a)
		if action == "" || a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func TestServerEndToEnd(t *testing.T) {
	e := newEnv(t, withDaemon, nil)

	resp, _ := e.raw("GET", "/api/v1/me", nil, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous /me: %d", resp.StatusCode)
	}
	op := e.login("alice@example.com", "operator")
	if strings.Join(op.me.Roles, ",") != "viewer,operator" || op.me.Deployment.Name != "test" || !op.me.Deployment.Daemon.Configured || !op.me.Deployment.Daemon.Reachable {
		t.Fatalf("me: %+v", op.me)
	}

	hdr := map[string]string{"Cookie": "cistern_session=" + op.cookie}
	if resp, _ := e.raw("POST", "/api/v1/jobs/app/backup", nil, hdr); resp.StatusCode != 403 {
		t.Fatalf("missing csrf: %d", resp.StatusCode)
	}
	hdr["X-Cistern-CSRF"] = op.csrf
	hdr["Content-Type"] = "text/plain"
	if resp, _ := e.raw("POST", "/api/v1/jobs/app/backup", strings.NewReader("x"), hdr); resp.StatusCode != 415 {
		t.Fatalf("non-json body: %d", resp.StatusCode)
	}
	op.expectError("POST", "/api/v1/jobs/nope/backup", nil, 404, codeNotFound)

	var ref struct {
		RunID string `json:"run_id"`
	}
	op.json("POST", "/api/v1/jobs/app/backup", nil, 202, &ref)
	run := op.wait(ref.RunID)
	if run.State != admin.StateSucceeded || run.Snapshot == "" || run.Trigger != admin.TriggerUI || run.RequestedBy.Email != "alice@example.com" || len(run.LogTail) == 0 {
		t.Fatalf("backup run: %+v", run)
	}
	op.json("POST", "/api/v1/jobs/broken/backup", nil, 202, &ref)
	if r := op.wait(ref.RunID); r.State != admin.StateFailed || !strings.Contains(r.Error, "dump crashed") {
		t.Fatalf("broken run: %+v", r)
	}
	op.json("POST", "/api/v1/jobs/app/verify", map[string]string{"snapshot": "latest"}, 202, &ref)
	if r := op.wait(ref.RunID); r.State != admin.StateSucceeded || r.Snapshot != run.Snapshot {
		t.Fatalf("verify run: %+v", r)
	}
	op.expectError("POST", "/api/v1/jobs/app/verify", map[string]string{"snapshot": "nope"}, 422, codeInvalid)

	var snaps struct {
		Snapshots []snapshotSummary `json:"snapshots"`
	}
	op.json("GET", "/api/v1/jobs/app/snapshots", nil, 200, &snaps)
	if len(snaps.Snapshots) != 1 || snaps.Snapshots[0].ID != run.Snapshot || snaps.Snapshots[0].LastVerify == nil ||
		snaps.Snapshots[0].LastVerify.Status != admin.StateSucceeded || strings.Join(snaps.Snapshots[0].KeptBy, ",") != "last" {
		t.Fatalf("snapshots: %+v", snaps)
	}
	var m map[string]any
	op.json("GET", "/api/v1/jobs/app/snapshots/"+run.Snapshot, nil, 200, &m)
	if m["id"] != run.Snapshot || m["format"].(float64) != 1 {
		t.Fatalf("manifest: %v", m)
	}
	var runs struct {
		Runs []admin.Run `json:"runs"`
	}
	op.json("GET", "/api/v1/jobs/app/runs?op=backup", nil, 200, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != run.ID {
		t.Fatalf("job runs: %+v", runs)
	}

	viewer := e.login("vic@example.com", "viewer")
	viewer.expectError("POST", "/api/v1/jobs/app/backup", nil, 403, codeForbidden)
	viewer.expectError("POST", "/api/v1/jobs/app/restore/preview", map[string]string{"snapshot": "latest", "target": "staging"}, 403, codeForbidden)
	if len(e.auditEntries(ActionAccessDenied)) != 2 {
		t.Fatalf("access.denied entries: %v", e.auditEntries(ActionAccessDenied))
	}

	rest := e.login("bob@example.com", "restorer")
	var prev restorePreview
	rest.json("POST", "/api/v1/jobs/app/restore/preview", map[string]string{"snapshot": run.Snapshot[:10], "target": "staging"}, 200, &prev)
	if prev.NeedsApproval || prev.Overrides["password"] != redacted || prev.Overrides["host"] != "staging-db" || prev.Snapshot.ID != run.Snapshot ||
		!strings.Contains(prev.CLICommand, "--set 'password=<redacted>'") || !strings.HasPrefix(prev.CLICommand, "cistern restore --job app --snapshot "+run.Snapshot) {
		t.Fatalf("preview: %+v", prev)
	}
	er := rest.expectError("POST", "/api/v1/jobs/app/restore/preview", map[string]any{"snapshot": "latest", "target": "elsewhere", "restore_suffix": "_x"}, 422, codeInvalid)
	if er.Error.Fields["target"] == "" {
		t.Fatalf("fields: %+v", er)
	}
	er = rest.expectError("POST", "/api/v1/jobs/app/restore/preview", map[string]any{"snapshot": "latest", "target": "staging", "restore_suffix": "_x"}, 422, codeInvalid)
	if er.Error.Fields["restore_suffix"] == "" {
		t.Fatalf("suffix must be rejected for this driver: %+v", er)
	}
	er = rest.expectError("POST", "/api/v1/jobs/app/restore", map[string]any{"snapshot": "latest", "target": "staging", "reason": "x", "confirm_job": "ap"}, 422, codeInvalid)
	if er.Error.Fields["reason"] == "" || er.Error.Fields["confirm_job"] == "" {
		t.Fatalf("fields: %+v", er)
	}
	var started struct {
		NeedsApproval bool   `json:"needs_approval"`
		RunID         string `json:"run_id"`
		ApprovalID    string `json:"approval_id"`
	}
	rest.json("POST", "/api/v1/jobs/app/restore", map[string]any{"snapshot": "latest", "target": "staging", "reason": "test the staging copy", "confirm_job": "app"}, 202, &started)
	if started.NeedsApproval || started.RunID == "" {
		t.Fatalf("restore: %+v", started)
	}
	if r := rest.wait(started.RunID); r.State != admin.StateSucceeded || r.Target != "staging" {
		t.Fatalf("restore run: %+v", r)
	}
	restoredMu.Lock()
	got := restored["app-staging"]
	restoredMu.Unlock()
	if got["host"] != "staging-db" || got["password"] != secrets["STAGING_PASSWORD"] {
		t.Fatalf("daemon must use its own resolved overrides, got %v", got["host"])
	}

	started.RunID = ""
	rest.json("POST", "/api/v1/jobs/app/restore", map[string]any{"snapshot": "latest", "target": "prod", "reason": "roll back bad migration", "confirm_job": "app"}, 202, &started)
	if !started.NeedsApproval || started.ApprovalID == "" || started.RunID != "" {
		t.Fatalf("prod restore: %+v", started)
	}
	rest.expectError("POST", "/api/v1/approvals/"+started.ApprovalID+"/approve", nil, 403, codeForbidden)

	self := e.login("bob@example.com", "approver,restorer")
	var ap approvalView
	self.json("GET", "/api/v1/approvals/"+started.ApprovalID, nil, 200, &ap)
	if ap.CanDecide || ap.State != ApprovalPending || ap.Target.Name != "prod" || !ap.Target.Production || ap.Snapshot != run.Snapshot {
		t.Fatalf("approval as requester: %+v", ap)
	}
	self.expectError("POST", "/api/v1/approvals/"+started.ApprovalID+"/approve", map[string]string{}, 403, codeForbidden)

	approver := e.login("carol@example.com", "approver")
	if strings.Join(approver.me.Roles, ",") != "viewer,approver" {
		t.Fatalf("approver roles: %v", approver.me.Roles)
	}
	var ov overviewResponse
	approver.json("GET", "/api/v1/overview", nil, 200, &ov)
	if len(ov.PendingApprovals) != 1 || !ov.PendingApprovals[0].CanDecide {
		t.Fatalf("pending approvals: %+v", ov.PendingApprovals)
	}
	approver.json("POST", "/api/v1/approvals/"+started.ApprovalID+"/approve", map[string]string{"note": "ok"}, 200, &ap)
	if ap.State != ApprovalApproved || ap.RunID == "" || ap.DecidedBy.Email != "carol@example.com" || ap.CanDecide {
		t.Fatalf("approved: %+v", ap)
	}
	if r := approver.wait(ap.RunID); r.State != admin.StateSucceeded || r.ApprovalID != started.ApprovalID || r.RequestedBy.Email != "bob@example.com" {
		t.Fatalf("approved run: %+v", r)
	}
	approver.expectError("POST", "/api/v1/approvals/"+started.ApprovalID+"/reject", map[string]string{"note": "too late"}, 409, codeConflict)

	rest.json("POST", "/api/v1/jobs/app/restore", map[string]any{"snapshot": "latest", "target": "prod", "reason": "second try", "confirm_job": "app"}, 202, &started)
	approver.expectError("POST", "/api/v1/approvals/"+started.ApprovalID+"/reject", map[string]string{"note": "no"}, 422, codeInvalid)
	ap = approvalView{}
	approver.json("POST", "/api/v1/approvals/"+started.ApprovalID+"/reject", map[string]string{"note": "not needed"}, 200, &ap)
	if ap.State != ApprovalRejected || ap.RunID != "" {
		t.Fatalf("rejected: %+v", ap)
	}
	var aps struct {
		Approvals []approvalView `json:"approvals"`
	}
	approver.json("GET", "/api/v1/approvals?state=decided", nil, 200, &aps)
	if len(aps.Approvals) != 2 {
		t.Fatalf("decided approvals: %+v", aps)
	}

	deadline := time.Now().Add(10 * time.Second)
	for len(e.auditEntries(ActionRunFinish)) < 5 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(e.auditEntries(ActionRunFinish)); n != 5 {
		t.Fatalf("run.finish entries: %d", n)
	}

	op.json("GET", "/api/v1/overview", nil, 200, &ov)
	health := map[string]string{}
	for _, j := range ov.Jobs {
		health[j.Name] = j.Health
	}
	if health["app"] != HealthHealthy || health["broken"] != HealthFailing || health["idle"] != HealthNeverRun {
		t.Fatalf("health: %v", health)
	}
	var app jobSummary
	for _, j := range ov.Jobs {
		if j.Name == "app" {
			app = j
		}
	}
	if app.Snapshots != 1 || app.StoredBytes == 0 || len(app.RecentBackups) != 1 || app.SourceLabel != "uitest 16.4" || app.LastVerify == nil {
		t.Fatalf("app summary: %+v", app)
	}
	kinds := map[string]bool{}
	for _, a := range ov.Alerts {
		kinds[a.Kind+"/"+a.Job] = true
	}
	if !kinds["backup_failing/broken"] || len(ov.RecentActivity) == 0 || len(ov.Repositories) != 2 {
		t.Fatalf("overview: %+v", ov)
	}

	var det jobDetail
	op.json("GET", "/api/v1/jobs/app", nil, 200, &det)
	if det.Source["host"] != "db.internal" || det.Source["password"] != "" || det.Source["user"] != "" || len(det.RestoreTargets) != 2 || det.Encryption.Mode != "none" {
		t.Fatalf("job detail: %+v", det)
	}
	var idle jobDetail
	op.json("GET", "/api/v1/jobs/idle", nil, 200, &idle)
	if len(idle.RestoreTargets) != 1 || idle.RestoreTargets[0].Name != "original" || !idle.RestoreTargets[0].Production {
		t.Fatalf("implicit target: %+v", idle.RestoreTargets)
	}

	var page struct {
		Entries    []AuditEntry `json:"entries"`
		NextCursor string       `json:"next_cursor"`
	}
	op.json("GET", "/api/v1/audit?limit=3", nil, 200, &page)
	if len(page.Entries) != 3 || page.NextCursor == "" || !page.Entries[0].Time.After(page.Entries[2].Time) {
		t.Fatalf("audit page: %+v", page)
	}
	var page2 struct {
		Entries []AuditEntry `json:"entries"`
	}
	op.json("GET", "/api/v1/audit?limit=3&cursor="+page.NextCursor, nil, 200, &page2)
	if len(page2.Entries) != 3 || page2.Entries[0].ID >= page.Entries[2].ID {
		t.Fatalf("audit page 2: %+v", page2)
	}
	op.json("GET", "/api/v1/audit?action=restore.request&job=app", nil, 200, &page)
	if len(page.Entries) != 2 || page.Entries[0].Result != ResultPending {
		t.Fatalf("filtered audit: %+v", page.Entries)
	}
	op.json("GET", "/api/v1/audit?q=carol", nil, 200, &page)
	if len(page.Entries) == 0 {
		t.Fatal("q filter found nothing")
	}
	op.expectError("GET", "/api/v1/audit?action=nope", nil, 422, codeInvalid)
	code, csvData := op.do("GET", "/api/v1/audit/export?format=csv", nil)
	if code != 200 || !strings.HasPrefix(string(csvData), "id,time,actor_id") || !strings.Contains(string(csvData), "approval.approve") {
		t.Fatalf("csv export: %d %s", code, csvData)
	}
	code, jl := op.do("GET", "/api/v1/audit/export?format=jsonl&action=backup.run", nil)
	if code != 200 || strings.Count(string(jl), "\n") != 2 {
		t.Fatalf("jsonl export: %d %s", code, jl)
	}

	stored := 0
	filepath.WalkDir(filepath.Join(e.dir, "repo", "_admin", "audit"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			stored++
		}
		return nil
	})
	if stored != len(e.auditEntries("")) {
		t.Fatalf("audit objects %d, stdout lines %d", stored, len(e.auditEntries("")))
	}

	op.json("POST", "/api/v1/auth/logout", nil, 204, nil)
	op.expectError("GET", "/api/v1/me", nil, 401, codeUnauthenticated)

	e.stop()
	var leaked []string
	check := func(where, s string) {
		for name, v := range secrets {
			if strings.Contains(s, v) {
				leaked = append(leaked, name+" in "+where)
			}
		}
	}
	check("responses", e.bodies.String())
	check("audit stdout", e.audit.String())
	check("logs", e.logs.String())
	filepath.WalkDir(filepath.Join(e.dir, "repo"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(p, "_admin") {
			data, _ := os.ReadFile(p)
			check(p, string(data))
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Fatalf("secrets leaked: %v", leaked)
	}
}

func TestReadOnlyWithoutDaemon(t *testing.T) {
	e := newEnv(t, noDaemon, nil)
	op := e.login("alice@example.com", "admin")
	if op.me.Deployment.Daemon.Configured {
		t.Fatal("daemon reported as configured")
	}
	op.expectError("POST", "/api/v1/jobs/app/backup", nil, 503, codeDaemon)
	var ov overviewResponse
	op.json("GET", "/api/v1/overview", nil, 200, &ov)
	if len(ov.Jobs) != 3 {
		t.Fatalf("overview: %+v", ov)
	}
}

func TestUnreachableDaemonAlert(t *testing.T) {
	e := newEnv(t, daemonDown, nil)
	op := e.login("alice@example.com", "operator")
	op.expectError("POST", "/api/v1/jobs/app/backup", nil, 503, codeDaemon)
	var ov overviewResponse
	op.json("GET", "/api/v1/overview", nil, 200, &ov)
	found := false
	for _, a := range ov.Alerts {
		found = found || a.Kind == "daemon_unreachable"
	}
	if !found {
		t.Fatalf("alerts: %+v", ov.Alerts)
	}
}

func TestStaticUIAndHeaders(t *testing.T) {
	shell := `<!doctype html><html><head><script>window.__x=1</script><script type="module" src="/assets/app-AbCd1234.js"></script></head><body></body></html>`
	ui := fstest.MapFS{
		"_shell.html":            {Data: []byte(shell)},
		"assets/app-AbCd1234.js": {Data: []byte("console.log(1)")},
		"favicon.ico":            {Data: []byte("ico")},
	}
	e := newEnv(t, noDaemon, ui)
	resp, body := e.raw("GET", "/jobs/app", nil, nil)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || string(body) != shell || resp.Header.Get("Cache-Control") != "no-cache" ||
		!strings.Contains(csp, "script-src 'self' 'sha256-") || strings.Contains(csp, "unsafe-inline' 'sha") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("shell: %d %v", resp.StatusCode, resp.Header)
	}
	if hashes := inlineScriptHashes([]byte(shell)); len(hashes) != 1 || !strings.Contains(csp, hashes[0]) {
		t.Fatalf("hashes: %v", hashes)
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS without TLS")
	}
	resp, body = e.raw("GET", "/assets/app-AbCd1234.js", nil, nil)
	if resp.StatusCode != 200 || string(body) != "console.log(1)" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := e.raw("GET", "/assets/missing-AbCd1234.js", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	resp, body = e.raw("GET", "/api/v1/nope", nil, nil)
	if resp.StatusCode != 404 || !strings.Contains(string(body), "not_found") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("api 404: %d %s", resp.StatusCode, body)
	}

	stub := newEnv(t, noDaemon, nil)
	resp, body = stub.raw("GET", "/", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "was not built") {
		t.Fatalf("stub page: %d %s", resp.StatusCode, body)
	}
}

func TestDevLoginRules(t *testing.T) {
	sc := &config.Server{Listen: "0.0.0.0:8480"}
	if sc.ValidateFor(true) == nil {
		t.Fatal("dev login allowed on a non-loopback address")
	}
	sc = &config.Server{Listen: "127.0.0.1:8480", TLS: config.ServerTLS{Upstream: true}}
	if sc.ValidateFor(true) == nil {
		t.Fatal("dev login allowed with TLS")
	}
	sc = &config.Server{Listen: "127.0.0.1:8480"}
	if err := sc.ValidateFor(true); err != nil {
		t.Fatal(err)
	}
	if sc.ValidateFor(false) == nil {
		t.Fatal("production mode without tls/oidc/session key accepted")
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/jobs/app": "/jobs/app", "": "/", "//evil.com": "/", "https://evil.com": "/", "/\\evil.com": "/", "jobs": "/", "/api/v1/me": "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSessionSealing(t *testing.T) {
	a, _ := newSealer([]byte(strings.Repeat("k", 32)), "session")
	b, _ := newSealer([]byte(strings.Repeat("j", 32)), "session")
	tok, _ := a.seal(&session{Subject: "x"}, "c")
	var s session
	if a.open(tok, "c", &s) != nil || s.Subject != "x" {
		t.Fatal("round trip")
	}
	if a.open(tok, "other", &s) == nil || b.open(tok, "c", &s) == nil || a.open(tok[:len(tok)-2]+"AA", "c", &s) == nil {
		t.Fatal("tampered or foreign token accepted")
	}
}

func TestIdleTimeout(t *testing.T) {
	e := newEnv(t, noDaemon, nil)
	u := e.login("alice@example.com", "viewer")
	now := time.Now()
	e.srv.now = func() time.Time { return now.Add(31 * time.Minute) }
	u.expectError("GET", "/api/v1/me", nil, 401, codeUnauthenticated)
	e.srv.now = func() time.Time { return now.Add(9 * time.Hour) }
	u.expectError("GET", "/api/v1/me", nil, 401, codeUnauthenticated)
}
