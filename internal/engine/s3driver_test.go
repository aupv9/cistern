package engine

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cistern/internal/config"
	"cistern/internal/crypt"
	_ "cistern/internal/driver/s3"
	"cistern/internal/manifest"
	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func s3DriverEngine(t *testing.T, srv *s3test.Server, withKey bool) (*Engine, *clock) {
	t.Helper()
	dir := t.TempDir()
	id, _ := crypt.GenerateIdentity()
	keyLine := ""
	if withKey {
		os.WriteFile(filepath.Join(dir, "id.txt"), []byte(id.String()+"\n"), 0o600)
		keyLine = "\n  identity_file: id.txt"
	}
	src := `
repositories:
  local: { type: local, path: repo }
encryption:
  recipients: [` + id.Recipient().String() + `]` + keyLine + `
scheduler: { timezone: UTC }
jobs:
  - name: site
    driver: s3
    timeout: 1m
    retention: { last: 1 }
    params:
      endpoint: ` + srv.URL + `
      bucket: ` + s3test.Bucket + `
      prefix: site/
      path_style: true
      part_size_mb: 5
      access_key_id: ` + s3test.AccessKey + `
      secret_access_key: ` + s3test.SecretKey + `
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
	return e, c
}

func s3client(t *testing.T, srv *s3test.Server) *s3.Client {
	c, err := s3.New(s3.Config{Endpoint: srv.URL, Bucket: s3test.Bucket, PathStyle: true, Credentials: s3test.Creds, PartSize: s3.MinPartSize})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func put(t *testing.T, c *s3.Client, key, body, ctype string) {
	t.Helper()
	if err := c.Upload(context.Background(), key, strings.NewReader(body), s3.PutOptions{ContentType: ctype}); err != nil {
		t.Fatal(err)
	}
}

func backupS3(t *testing.T, e *Engine, c *clock) *manifest.Manifest {
	t.Helper()
	c.t = c.t.Add(time.Hour)
	m, err := e.Backup(context.Background(), "site")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestS3DriverIncremental(t *testing.T) {
	ctx := context.Background()
	srv := s3test.New(false)
	defer srv.Close()
	src := s3client(t, srv)
	big := make([]byte, 6<<20)
	rand.Read(big)
	put(t, src, "site/a.txt", "version 1", "text/plain")
	put(t, src, "site/dir/b c.txt", "to be deleted", "")
	put(t, src, "site/ảnh đẹp.jpg", "jpeg bytes", "image/jpeg")
	put(t, src, "site/empty", "", "")
	put(t, src, "site/big.bin", string(big), "")
	put(t, src, "other/outside.txt", "not in prefix", "")

	e, c := s3DriverEngine(t, srv, true)
	m1 := backupS3(t, e, c)
	if m1.Source.Extra["copied"] != "5" || m1.Source.Extra["reused"] != "0" || len(m1.Artifacts) != 6 || len(m1.DependsOn) != 0 {
		t.Fatalf("backup 1: extra=%v artifacts=%d deps=%v", m1.Source.Extra, len(m1.Artifacts), m1.DependsOn)
	}

	// Sửa một object, thêm một, xóa một.
	put(t, src, "site/a.txt", "version 2", "text/plain")
	put(t, src, "site/new.txt", "added", "")
	src.DeleteObject(ctx, "site/dir/b c.txt")
	m2 := backupS3(t, e, c)
	if m2.Source.Extra["copied"] != "2" || m2.Source.Extra["reused"] != "3" || len(m2.Artifacts) != 3 ||
		!reflect.DeepEqual(m2.DependsOn, []string{m1.ID}) || !strings.HasPrefix(m2.Source.Extra["mode"], "incremental") {
		t.Fatalf("backup 2: extra=%v artifacts=%d deps=%v", m2.Source.Extra, len(m2.Artifacts), m2.DependsOn)
	}
	// Không đổi gì: chỉ ghi index; dữ liệu trỏ thẳng tới snapshot đang giữ nó (không qua m2 khi không cần).
	m3 := backupS3(t, e, c)
	if m3.Source.Extra["copied"] != "0" || len(m3.Artifacts) != 1 || !reflect.DeepEqual(m3.DependsOn, []string{m1.ID, m2.ID}) {
		t.Fatalf("backup 3: extra=%v artifacts=%d deps=%v", m3.Source.Extra, len(m3.Artifacts), m3.DependsOn)
	}
	if rep, err := e.Verify(ctx, "site", "all"); err != nil || rep.Failures != 0 {
		t.Fatalf("verify: %+v %v", rep, err)
	}

	read := func(key string) string {
		rc, err := src.GetObject(ctx, key)
		if err != nil {
			return "<missing>"
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b)
	}

	// Restore bản mới nhất sang prefix khác: đúng trạng thái hiện tại.
	if _, err := e.Restore(ctx, "site", "latest", map[string]string{"restore_prefix": "restored/"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a.txt", "ảnh đẹp.jpg", "empty", "big.bin", "new.txt"} {
		if read("restored/"+rel) != read("site/"+rel) {
			t.Errorf("restored/%s differs", rel)
		}
	}
	if read("restored/dir/b c.txt") != "<missing>" || read("restored/outside.txt") != "<missing>" {
		t.Error("restored objects that were not in the snapshot")
	}
	if v, _ := srv.Current("restored/ảnh đẹp.jpg"); v.ContentType != "image/jpeg" {
		t.Errorf("content type: %q", v.ContentType)
	}

	// Restore bản đầu: object đã xóa quay lại, a.txt là phiên bản cũ.
	if _, err := e.Restore(ctx, "site", m1.ID, map[string]string{"restore_prefix": "v1/"}); err != nil {
		t.Fatal(err)
	}
	if read("v1/a.txt") != "version 1" || read("v1/dir/b c.txt") != "to be deleted" || read("v1/new.txt") != "<missing>" {
		t.Errorf("v1: a=%q b=%q new=%q", read("v1/a.txt"), read("v1/dir/b c.txt"), read("v1/new.txt"))
	}

	// Ghi đè bị từ chối nếu không có overwrite=true.
	if _, err := e.Restore(ctx, "site", m1.ID, nil); err == nil || !strings.Contains(err.Error(), "already exist") {
		t.Fatalf("overwrite not refused: %v", err)
	}
	if _, err := e.Restore(ctx, "site", m1.ID, map[string]string{"overwrite": "true", "only": "site/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if read("site/a.txt") != "version 1" || read("site/new.txt") != "added" {
		t.Errorf("only+overwrite: a=%q new=%q", read("site/a.txt"), read("site/new.txt"))
	}

	// Prune (last: 1) phải giữ m1, m2 vì m3 phụ thuộc chúng.
	rep, err := e.Prune(ctx, "site", false)
	if err != nil || rep.Removed != 0 {
		t.Fatalf("prune: %+v %v", rep, err)
	}
	for _, it := range rep.Items {
		if it.Snapshot != m3.ID && !reflect.DeepEqual(it.Reasons, []string{"depends_on"}) {
			t.Errorf("%s kept for %v", it.Snapshot, it.Reasons)
		}
	}
}

func TestS3DriverObjectChangedDuringCopy(t *testing.T) {
	srv := s3test.New(false)
	defer srv.Close()
	src := s3client(t, srv)
	put(t, src, "site/a.txt", "one", "")
	put(t, src, "site/b.txt", "two", "")
	var once sync.Once
	// Object bị sửa sau khi liệt kê, ngay trước khi tải về.
	srv.BeforeGet = func(key string) {
		if key == "site/b.txt" {
			once.Do(func() { src.Upload(context.Background(), "site/b.txt", strings.NewReader("changed"), s3.PutOptions{}) })
		}
	}
	e, c := s3DriverEngine(t, srv, true)
	c.t = c.t.Add(time.Hour)
	_, err := e.Backup(context.Background(), "site")
	if err == nil || !strings.Contains(err.Error(), "changed during backup") {
		t.Fatalf("err = %v", err)
	}
	if list, _ := e.List(context.Background(), "site"); len(list) != 0 {
		t.Fatal("snapshot recorded despite changed object")
	}
}

func TestS3DriverFullWithoutPrivateKey(t *testing.T) {
	srv := s3test.New(false)
	defer srv.Close()
	put(t, s3client(t, srv), "site/a.txt", "x", "")
	e, c := s3DriverEngine(t, srv, false)
	backupS3(t, e, c)
	m := backupS3(t, e, c)
	if m.Source.Extra["copied"] != "1" || !strings.HasPrefix(m.Source.Extra["mode"], "full") || len(m.DependsOn) != 0 {
		t.Fatalf("expected a full copy: %v deps=%v", m.Source.Extra, m.DependsOn)
	}
}
