package metrics

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteText(t *testing.T) {
	r := NewRegistry()
	r.Set(BackupLastSuccess, Job("orders"), 1.7e9)
	r.Add(BackupFailures, Job("orders"), 1)
	r.Add(BackupFailures, Job("orders"), 1)
	r.Set(BackupLastSuccess, Job(`we"ird`), 5)
	var b bytes.Buffer
	r.WriteText(&b, "")
	out := b.String()
	for _, want := range []string{
		"# TYPE cistern_backup_failures_total counter\n",
		`cistern_backup_failures_total{cistern_job="orders"} 2` + "\n",
		`cistern_backup_last_success_timestamp_seconds{cistern_job="orders"} 1700000000` + "\n",
		`cistern_backup_last_success_timestamp_seconds{cistern_job="we\"ird"} 5` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	b.Reset()
	r.WriteText(&b, "orders")
	if strings.Contains(b.String(), "ird") {
		t.Error("job filter leaked other job")
	}
}

func TestPush(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method, path = req.Method, req.URL.Path
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}))
	defer srv.Close()
	r := NewRegistry()
	r.Set(BackupLastFailure, Job("orders"), 42)
	r.Set(BackupLastFailure, Job("other"), 1)
	if err := r.Push(context.Background(), srv.URL+"/", "orders"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/metrics/job/cistern/instance/orders" {
		t.Errorf("%s %s", method, path)
	}
	if !strings.Contains(body, `{cistern_job="orders"} 42`) || strings.Contains(body, "other") {
		t.Errorf("body:\n%s", body)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", 400) }))
	defer bad.Close()
	if err := r.Push(context.Background(), bad.URL, "orders"); err == nil {
		t.Error("expected error on 400")
	}
}

func TestTextfileKeepsPreviousSuccess(t *testing.T) {
	dir := t.TempDir()
	ok := NewRegistry()
	ok.Set(BackupLastSuccess, Job("orders"), 100)
	ok.Set(BackupLastSize, Job("orders"), 2048)
	if err := ok.WriteTextfile(dir, "orders"); err != nil {
		t.Fatal(err)
	}
	fail := NewRegistry()
	fail.Set(BackupLastFailure, Job("orders"), 200)
	fail.Add(BackupFailures, Job("orders"), 1)
	if err := fail.WriteTextfile(dir, "orders"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "cistern_orders.prom"))
	out := string(b)
	for _, want := range []string{
		`cistern_backup_last_success_timestamp_seconds{cistern_job="orders"} 100`,
		`cistern_backup_last_size_bytes{cistern_job="orders"} 2048`,
		`cistern_backup_last_failure_timestamp_seconds{cistern_job="orders"} 200`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "# TYPE cistern_backup_last_success_timestamp_seconds") != 1 {
		t.Errorf("duplicate TYPE lines:\n%s", out)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
}
