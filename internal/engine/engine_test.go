package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cistern/internal/config"
	"cistern/internal/crypt"
	"cistern/internal/driver"
	"cistern/internal/manifest"
)

// fakeDriver ghi các artifact theo params và lưu nội dung khi restore.
type fakeDriver struct{ p driver.Params }

var (
	restored map[string]string
	blockCh  chan struct{}
)

func init() {
	driver.Register("fake", func(p driver.Params) (driver.Driver, error) { return &fakeDriver{p: p}, nil })
}

func (f *fakeDriver) Name() string                { return "fake" }
func (f *fakeDriver) Check(context.Context) error { return nil }

func (f *fakeDriver) Backup(ctx context.Context, s driver.Sink) (driver.Info, error) {
	for _, name := range strings.Split(f.p["artifacts"], ",") {
		w, err := s.Create(name)
		if err != nil {
			return driver.Info{}, err
		}
		if name == f.p["fail_on"] {
			w.Write([]byte("partial"))
			return driver.Info{}, errors.New("dump crashed")
		}
		if f.p["block"] == "true" {
			blockCh <- struct{}{}
			<-blockCh
		}
		data := bytes.Repeat([]byte(name+":"), 50000) // ~ vài trăm KiB, nhiều khối age
		if name == f.p["big"] {
			// Dữ liệu ngẫu nhiên không nén được, để bản lưu vượt một part S3.
			n, _ := strconv.Atoi(f.p["big_size"])
			noise := make([]byte, n)
			rand.Read(noise)
			data = append(data, noise...)
		}
		if _, err := w.Write(data); err != nil {
			return driver.Info{}, err
		}
		if err := w.Close(); err != nil {
			return driver.Info{}, err
		}
	}
	return driver.Info{Version: "1.0", Host: "fakehost"}, nil
}

func (f *fakeDriver) Restore(ctx context.Context, src driver.Source) error {
	for _, name := range src.List() {
		rc, err := src.Open(name)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		restored[name+f.p["suffix"]] = string(b)
	}
	return nil
}

func setup(t *testing.T, encrypted bool) (*Engine, string, *crypt.Identity) {
	t.Helper()
	dir := t.TempDir()
	id, _ := crypt.GenerateIdentity()
	enc := "allow_plaintext: true"
	if encrypted {
		os.WriteFile(filepath.Join(dir, "id.txt"), []byte(id.String()+"\n"), 0o600)
		enc = "recipients: [" + id.Recipient().String() + "]\n  identity_file: id.txt"
	}
	src := `
repositories:
  local: { type: local, path: repo }
encryption:
  ` + enc + `
jobs:
  - name: app
    driver: fake
    timeout: 1m
    params: { artifacts: "db/a.dump,db/b.dump,globals.sql" }
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
	restored = map[string]string{}
	return e, filepath.Join(dir, "repo"), id
}

func TestBackupVerifyRestore(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		e, repoDir, _ := setup(t, encrypted)
		ctx := context.Background()
		m, err := e.Backup(ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		wantSuffix := ".gz"
		if encrypted {
			wantSuffix = ".gz.age"
		}
		if len(m.Artifacts) != 3 || !strings.HasSuffix(m.Artifacts[0].Path, wantSuffix) || m.Source.Host != "fakehost" {
			t.Fatalf("manifest: %+v", m)
		}
		if _, err := os.Stat(filepath.Join(repoDir, "locks", "app.lock")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("lock not released")
		}

		time.Sleep(1100 * time.Millisecond) // ID theo giây
		if _, err := e.Backup(ctx, "app"); err != nil {
			t.Fatal(err)
		}
		list, err := e.List(ctx, "app")
		if err != nil || len(list) != 2 || list[0].ID != m.ID {
			t.Fatalf("list: %v %v", list, err)
		}

		rep, err := e.Verify(ctx, "app", "all")
		if err != nil || rep.Failures != 0 || len(rep.Results) != 6 || rep.Results[0].Mode != "full" {
			t.Fatalf("verify: %+v %v", rep, err)
		}

		if _, err := e.Restore(ctx, "app", m.ID[:10], map[string]string{"suffix": "_r"}); err == nil {
			t.Fatal("ambiguous prefix accepted")
		}
		if _, err := e.Restore(ctx, "app", m.ID, map[string]string{"suffix": "_r"}); err != nil {
			t.Fatal(err)
		}
		if got := restored["db/a.dump_r"]; got != strings.Repeat("db/a.dump:", 50000) {
			t.Fatalf("restored %d bytes", len(got))
		}

		// Sửa 1 byte trong bản lưu → verify và restore đều báo lỗi.
		art := filepath.Join(repoDir, "app", m.ID, filepath.FromSlash(m.Artifacts[1].Path))
		b, _ := os.ReadFile(art)
		b[len(b)/2] ^= 1
		os.WriteFile(art, b, 0o600)
		rep, _ = e.Verify(ctx, "app", m.ID)
		if rep.Failures != 1 || rep.Results[1].OK {
			t.Fatalf("corruption not detected: %+v", rep)
		}
		if _, err := e.Restore(ctx, "app", m.ID, nil); err == nil {
			t.Fatal("restore of corrupted snapshot succeeded")
		}
	}
}

func TestVerifyWithoutPrivateKey(t *testing.T) {
	e, _, _ := setup(t, true)
	ctx := context.Background()
	if _, err := e.Backup(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Encryption.IdentityFile = ""
	e.idSet = false
	rep, err := e.Verify(ctx, "app", "latest")
	if err != nil || rep.Failures != 0 || rep.Results[0].Mode != "stored" {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if _, err := e.Restore(ctx, "app", "latest", nil); err == nil || !strings.Contains(err.Error(), "identity_file") {
		t.Fatalf("restore without key: %v", err)
	}
}

func TestFailedBackupLeavesNothing(t *testing.T) {
	e, repoDir, _ := setup(t, true)
	j, _ := e.Cfg.Job("app")
	j.Params["fail_on"] = "db/b.dump"
	if _, err := e.Backup(context.Background(), "app"); err == nil || !strings.Contains(err.Error(), "dump crashed") {
		t.Fatalf("err = %v", err)
	}
	var files []string
	filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("leftover files: %v", files)
	}
}

func TestLock(t *testing.T) {
	e, repoDir, _ := setup(t, false)
	j, _ := e.Cfg.Job("app")
	j.Params["block"] = "true"
	blockCh = make(chan struct{})
	errc := make(chan error, 1)
	go func() { _, err := e.Backup(context.Background(), "app"); errc <- err }()
	<-blockCh // backup đầu đang giữ khóa

	// Lần chạy thứ hai bị chặn ở khóa, trước khi driver chạy.
	if _, err := e.Backup(context.Background(), "app"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("second backup: %v", err)
	}
	for i := 0; i < 3; i++ { // 3 artifact, mỗi cái chặn một lần
		blockCh <- struct{}{}
		if i < 2 {
			<-blockCh
		}
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	// Khóa hết hạn bị gỡ.
	stale, _ := json.Marshal(manifest.Lock{Host: "dead", PID: 1, Op: "backup", Expires: time.Now().Add(-time.Hour)})
	os.MkdirAll(filepath.Join(repoDir, "locks"), 0o700)
	os.WriteFile(filepath.Join(repoDir, "locks", "app.lock"), stale, 0o600)
	j.Params["block"] = "false"
	if _, err := e.Backup(context.Background(), "app"); err != nil {
		t.Fatalf("stale lock not removed: %v", err)
	}
}
