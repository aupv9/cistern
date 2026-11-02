package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cistern/internal/config"
	"cistern/internal/driver"
	"cistern/internal/manifest"
	"cistern/internal/metrics"
)

// forgetDriver: như fakeDriver nhưng có dữ liệu "ngoài repository" (giống Elasticsearch).
type forgetDriver struct {
	fakeDriver
}

var (
	forgetMu  sync.Mutex
	forgotten []string
	seq       int
)

func init() {
	driver.Register("fakeforget", func(p driver.Params) (driver.Driver, error) { return &forgetDriver{fakeDriver{p: p}}, nil })
}

func (f *forgetDriver) Backup(ctx context.Context, s driver.Sink) (driver.Info, error) {
	info, err := f.fakeDriver.Backup(ctx, s)
	forgetMu.Lock()
	seq++
	info.Extra = map[string]string{"seq": strconv.Itoa(seq)}
	forgetMu.Unlock()
	return info, err
}

func (f *forgetDriver) Forget(_ context.Context, info driver.Info) error {
	if info.Extra["seq"] == f.p["forget_fail"] {
		return errors.New("snapshot API unavailable")
	}
	forgetMu.Lock()
	forgotten = append(forgotten, info.Extra["seq"])
	forgetMu.Unlock()
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newEngine(t *testing.T, jobYAML, extraYAML string) (*Engine, string, *clock) {
	t.Helper()
	dir := t.TempDir()
	src := `
repositories:
  local: { type: local, path: repo }
encryption:
  allow_plaintext: true
scheduler:
  timezone: UTC
` + extraYAML + `
jobs:
  - name: app
    timeout: 1m
` + jobYAML
	cfgPath := filepath.Join(dir, "cistern.yaml")
	os.WriteFile(cfgPath, []byte(src), 0o600)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)}
	e.now = c.now
	restored = map[string]string{}
	return e, filepath.Join(dir, "repo"), c
}

func backupDays(t *testing.T, e *Engine, c *clock, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := e.Backup(context.Background(), "app"); err != nil {
			t.Fatal(err)
		}
		c.t = c.t.Add(24 * time.Hour)
	}
}

func TestPruneGFSAndDryRun(t *testing.T) {
	e, _, c := newEngine(t, `    driver: fake
    retention: { last: 2, daily: 4 }
    params: { artifacts: "a" }
`, "")
	ctx := context.Background()
	backupDays(t, e, c, 10)

	rep, err := e.Prune(ctx, "app", true)
	if err != nil {
		t.Fatal(err)
	}
	removes := 0
	for _, it := range rep.Items {
		if it.Action == ActionRemove {
			removes++
		}
	}
	if removes != 6 || rep.Removed != 6 || rep.Kept != 4 {
		t.Fatalf("dry run: removes=%d %+v", removes, rep)
	}
	if list, _ := e.List(ctx, "app"); len(list) != 10 {
		t.Fatalf("dry run deleted snapshots: %d left", len(list))
	}

	rep, err = e.Prune(ctx, "app", false)
	if err != nil || rep.Removed != 6 || rep.Errors != 0 {
		t.Fatalf("prune: %+v %v", rep, err)
	}
	list, _ := e.List(ctx, "app")
	if len(list) != 4 || !strings.HasPrefix(list[0].ID, "20261007") || !strings.HasPrefix(list[3].ID, "20261010") {
		ids := []string{}
		for _, m := range list {
			ids = append(ids, m.ID)
		}
		t.Fatalf("kept: %v", ids)
	}
	if v, _ := e.Metrics.Get(metrics.Snapshots, metrics.Job("app")); v != 4 {
		t.Errorf("cistern_snapshots = %v", v)
	}
	if v, _ := e.Metrics.Get(metrics.PruneRemoved, metrics.Job("app")); v != 6 {
		t.Errorf("prune removed = %v", v)
	}
}

func TestPruneIncompleteAndNoPolicy(t *testing.T) {
	e, repoDir, c := newEngine(t, `    driver: fake
    params: { artifacts: "a" }
`, "")
	ctx := context.Background()
	backupDays(t, e, c, 2)
	r, _, _ := e.repoFor(e.Cfg.Jobs[0])
	old := manifest.NewSnapshotID(c.t.Add(-2 * time.Hour))
	fresh := manifest.NewSnapshotID(c.t.Add(-10 * time.Minute))
	for _, id := range []string{old, fresh} {
		r.Put(ctx, "app/"+id+"/artifacts/a.gz", strings.NewReader("partial"))
	}

	rep, err := e.Prune(ctx, "app", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "app", old)); !errors.Is(err, os.ErrNotExist) {
		t.Error("old incomplete snapshot not removed")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "app", fresh)); err != nil {
		t.Error("recent incomplete snapshot removed (backup may still be running)")
	}
	if list, _ := e.List(ctx, "app"); len(list) != 2 {
		t.Errorf("no policy must keep everything, got %d", len(list))
	}
	if rep.Kept != 3 { // 2 hoàn tất + 1 bản dở còn mới
		t.Errorf("report: %+v", rep)
	}
}

func TestPruneAfterBackupAndForget(t *testing.T) {
	forgotten = nil
	e, _, c := newEngine(t, `    driver: fakeforget
    retention: { last: 2 }
    prune_after_backup: true
    params: { artifacts: "a", forget_fail: "-" }
`, "")
	ctx := context.Background()
	start := seq
	backupDays(t, e, c, 4)
	if list, _ := e.List(ctx, "app"); len(list) != 2 {
		t.Fatalf("prune_after_backup: %d snapshots", len(list))
	}
	if len(forgotten) != 2 || forgotten[0] != strconv.Itoa(start+1) {
		t.Fatalf("forgotten: %v", forgotten)
	}

	// Forget lỗi → snapshot được giữ, prune báo lỗi.
	e.Cfg.Jobs[0].Params["forget_fail"] = strconv.Itoa(start + 3)
	e.Cfg.Jobs[0].Retention.Last = 1
	rep, err := e.Prune(ctx, "app", false)
	if err != nil || rep.Errors != 1 || rep.Removed != 0 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if list, _ := e.List(ctx, "app"); len(list) != 2 {
		t.Fatalf("snapshot with failed forget was removed")
	}
}

func TestPruneRespectsLock(t *testing.T) {
	e, repoDir, _ := newEngine(t, `    driver: fake
    retention: { last: 1 }
    params: { artifacts: "a" }
`, "")
	lock, _ := json.Marshal(manifest.Lock{Host: "other", PID: 7, Op: "backup", Expires: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	os.MkdirAll(filepath.Join(repoDir, "locks"), 0o700)
	os.WriteFile(filepath.Join(repoDir, "locks", "app.lock"), lock, 0o600)
	if _, err := e.Prune(context.Background(), "app", false); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.Prune(context.Background(), "app", true); err != nil {
		t.Fatalf("dry run should not need the lock: %v", err)
	}
}

type hookSink struct {
	mu     sync.Mutex
	events []map[string]any
}

func (h *hookSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	h.mu.Lock()
	h.events = append(h.events, m)
	h.mu.Unlock()
}

func TestMetricsAndWebhooks(t *testing.T) {
	hooks := &hookSink{}
	srv := httptest.NewServer(hooks)
	defer srv.Close()
	textDir := t.TempDir()
	e, _, c := newEngine(t, `    driver: fake
    params: { artifacts: "a,b", fail_on: "b" }
`, `metrics: { textfile_dir: `+textDir+` }
notify:
  webhooks:
    - { url: `+srv.URL+`, format: json, on: [failure, success] }
`)
	e.Publish = true
	ctx := context.Background()
	prom := filepath.Join(textDir, "cistern_app.prom")

	// Lỗi: failure metrics + webhook failure.
	if _, err := e.Backup(ctx, "app"); err == nil {
		t.Fatal("expected failure")
	}
	b, _ := os.ReadFile(prom)
	if !strings.Contains(string(b), `cistern_backup_last_failure_timestamp_seconds{cistern_job="app"}`) {
		t.Fatalf("textfile after failure:\n%s", b)
	}
	if len(hooks.events) != 1 || hooks.events[0]["status"] != "failure" || !strings.Contains(hooks.events[0]["error"].(string), "dump crashed") {
		t.Fatalf("events: %v", hooks.events)
	}

	// Thành công: success metrics, kích thước, failure cũ vẫn còn trong textfile.
	e.Cfg.Jobs[0].Params["fail_on"] = ""
	c.t = c.t.Add(time.Hour)
	m, err := e.Backup(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(prom)
	out := string(b)
	for _, want := range []string{
		`cistern_backup_last_success_timestamp_seconds{cistern_job="app"} ` + strconv.FormatFloat(unix(m.FinishedAt), 'f', -1, 64),
		`cistern_backup_last_size_bytes{cistern_job="app"} ` + strconv.FormatInt(storedSize(m), 10),
		`cistern_snapshots{cistern_job="app"} 1`,
		`cistern_backup_last_failure_timestamp_seconds{cistern_job="app"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in textfile:\n%s", want, out)
		}
	}
	if len(hooks.events) != 2 || hooks.events[1]["status"] != "success" || hooks.events[1]["snapshot"] != m.ID {
		t.Fatalf("events: %v", hooks.events)
	}

	// Verify thành công cập nhật verify_last_success.
	if _, err := e.Verify(ctx, "app", "latest"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Metrics.Get(metrics.VerifyLastSuccess, metrics.Job("app")); !ok {
		t.Error("verify success metric missing")
	}
}
