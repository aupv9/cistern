package engine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cistern/internal/config"
	"cistern/internal/crypt"
	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func newS3Engine(t *testing.T, srv *s3test.Server, lock string) (*Engine, *clock) {
	t.Helper()
	dir := t.TempDir()
	id, _ := crypt.GenerateIdentity()
	os.WriteFile(filepath.Join(dir, "id.txt"), []byte(id.String()+"\n"), 0o600)
	src := `
repositories:
  s3main:
    type: s3
    endpoint: ` + srv.URL + `
    bucket: ` + s3test.Bucket + `
    prefix: cistern
    path_style: true
    access_key_id: ` + s3test.AccessKey + `
    secret_access_key: ` + s3test.SecretKey + `
    part_size_mb: 5
` + lock + `
encryption:
  recipients: [` + id.Recipient().String() + `]
  identity_file: id.txt
scheduler: { timezone: UTC }
jobs:
  - name: app
    driver: fake
    timeout: 1m
    retention: { last: 1 }
    params: { artifacts: "db/a.dump,db/big.dump" }
`
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
	c := &clock{t: time.Now().UTC()}
	e.now = c.now
	restored = map[string]string{}
	return e, c
}

func TestS3RepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := s3test.New(true)
	defer srv.Close()
	e, c := newS3Engine(t, srv, "    object_lock: { mode: COMPLIANCE, days: 3 }")

	j := e.Cfg.Jobs[0]
	j.Params["big"], j.Params["big_size"] = "db/big.dump", "6000000"

	if err := e.Check(ctx, "app"); err != nil {
		t.Fatalf("check: %v", err)
	}
	m1, err := e.Backup(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	keys := dataKeys(srv.Keys())
	if len(keys) != 3 || !strings.HasPrefix(keys[0], "cistern/app/"+m1.ID+"/") {
		t.Fatalf("keys: %v", keys)
	}
	for _, k := range keys {
		v, _ := srv.Current(k)
		if v.LockMode != s3.LockCompliance || v.RetainUntil.Before(time.Now().Add(71*time.Hour)) {
			t.Errorf("%s not locked: %s %v", k, v.LockMode, v.RetainUntil)
		}
	}
	multipart := false
	for _, req := range srv.Requests() {
		multipart = multipart || strings.Contains(req, "uploads")
	}
	if !multipart {
		t.Error("expected a multipart upload for big.dump")
	}
	if len(srv.Versions("cistern/locks/app.lock")) != 2 {
		t.Errorf("lock versions: %+v", srv.Versions("cistern/locks/app.lock"))
	}

	if rep, err := e.Verify(ctx, "app", "latest"); err != nil || rep.Failures != 0 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if _, err := e.Restore(ctx, "app", "latest", map[string]string{"suffix": "_r"}); err != nil {
		t.Fatal(err)
	}
	if got := restored["db/a.dump_r"]; !strings.HasPrefix(got, "db/a.dump:db/a.dump:") {
		t.Fatalf("restored %d bytes", len(got))
	}

	c.t = c.t.Add(time.Hour)
	if _, err := e.Backup(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	rep, err := e.Prune(ctx, "app", false)
	if err != nil || rep.Removed != 1 {
		t.Fatalf("prune: %+v %v", rep, err)
	}
	if list, _ := e.List(ctx, "app"); len(list) != 1 || list[0].ID == m1.ID {
		t.Fatalf("list after prune: %v", list)
	}
	mk := "cistern/app/" + m1.ID + "/manifest.json"
	vs := srv.Versions(mk)
	if len(vs) != 2 || !vs[1].DeleteMarker || vs[0].LockMode != s3.LockCompliance {
		t.Fatalf("pruned manifest versions: %+v", vs)
	}
}

func TestS3BackupFailureCleansUp(t *testing.T) {
	ctx := context.Background()
	srv := s3test.New(false)
	defer srv.Close()
	e, _ := newS3Engine(t, srv, "")
	e.Cfg.Jobs[0].Params["big"], e.Cfg.Jobs[0].Params["big_size"] = "db/a.dump", "6000000"
	e.Cfg.Jobs[0].Params["fail_on"] = "db/big.dump"
	if _, err := e.Backup(ctx, "app"); err == nil {
		t.Fatal("expected failure")
	}
	if keys := dataKeys(srv.Keys()); len(keys) != 0 {
		t.Fatalf("leftover keys: %v", keys)
	}
	if srv.OpenUploads() != 0 {
		t.Fatal("multipart upload left open")
	}
}

func TestS3CheckRequiresObjectLock(t *testing.T) {
	srv := s3test.New(false)
	defer srv.Close()
	e, _ := newS3Engine(t, srv, "    object_lock: { mode: GOVERNANCE, days: 1 }")
	if err := e.Check(context.Background(), "app"); err == nil || !strings.Contains(err.Error(), "Object Lock") {
		t.Fatalf("check: %v", err)
	}
	if _, err := e.Backup(context.Background(), "app"); err == nil {
		t.Fatal("backup with retention to a bucket without Object Lock must fail")
	}
}

func dataKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if !strings.Contains(k, "/_admin/") {
			out = append(out, k)
		}
	}
	return out
}
