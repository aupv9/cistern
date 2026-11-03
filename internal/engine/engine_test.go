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

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/crypt"
	"cistern/internal/driver"
	"cistern/internal/manifest"
)

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
		data := bytes.Repeat([]byte(name+":"), 50000)
		if name == f.p["big"] {

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

		time.Sleep(1100 * time.Millisecond)
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
		if err == nil && !d.IsDir() && !strings.Contains(filepath.ToSlash(p), "/_admin/") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("leftover files: %v", files)
	}
	r, prefix, _ := e.repoFor(j)
	recs, err := admin.ListRecords(context.Background(), r, prefix, "app")
	if err != nil || len(recs) != 1 || recs[0].Status != admin.StateFailed || recs[0].Op != admin.OpBackup ||
		recs[0].Trigger != admin.TriggerCLI || !strings.Contains(recs[0].Error, "dump crashed") || recs[0].Snapshot != "" {
		t.Fatalf("run records: %+v, %v", recs, err)
	}
}

func TestRunRecordsAndProgress(t *testing.T) {
	e, _, _ := setup(t, true)
	actor := &admin.Actor{ID: "u1", Email: "a@example.com", Name: "A"}
	prog := &admin.Progress{}
	id := admin.NewID(time.Now())
	ctx := WithRun(context.Background(), &RunInfo{ID: id, Trigger: admin.TriggerUI, RequestedBy: actor, Progress: prog})
	m, err := e.Backup(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if v := prog.View(); v.ArtifactsDone != 3 || v.BytesDone == 0 {
		t.Fatalf("backup progress: %+v", v)
	}
	vprog := &admin.Progress{}
	if _, err := e.Verify(WithRun(context.Background(), &RunInfo{Trigger: admin.TriggerSchedule, Progress: vprog}), "app", "latest"); err != nil {
		t.Fatal(err)
	}
	if v := vprog.View(); v.ArtifactsDone != 3 || v.ArtifactsTotal != 3 || v.BytesDone != v.BytesTotal {
		t.Fatalf("verify progress: %+v", v)
	}
	rprog := &admin.Progress{}
	if _, err := e.Restore(WithRun(context.Background(), &RunInfo{Trigger: admin.TriggerUI, Target: "staging", ApprovalID: "ap1", Progress: rprog}), "app", "latest", map[string]string{"suffix": "_x"}); err != nil {
		t.Fatal(err)
	}
	if v := rprog.View(); v.ArtifactsDone != 3 || v.BytesDone != v.BytesTotal || v.BytesTotal == 0 {
		t.Fatalf("restore progress: %+v", v)
	}
	j, _ := e.Cfg.Job("app")
	r, prefix, _ := e.repoFor(j)
	recs, err := admin.ListRecords(context.Background(), r, prefix, "app")
	if err != nil || len(recs) != 3 {
		t.Fatalf("records: %+v %v", recs, err)
	}
	byOp := map[string]*admin.Record{}
	for _, rec := range recs {
		byOp[rec.Op] = rec
	}
	b := byOp[admin.OpBackup]
	if b.ID != id || b.Status != admin.StateSucceeded || b.Snapshot != m.ID || b.StoredBytes == 0 || b.RequestedBy.Email != "a@example.com" || b.Trigger != admin.TriggerUI {
		t.Fatalf("backup record: %+v", b)
	}
	if v := byOp[admin.OpVerify]; v.Snapshot != m.ID || v.Trigger != admin.TriggerSchedule || v.RequestedBy.ID != "scheduler" || v.Status != admin.StateSucceeded {
		t.Fatalf("verify record: %+v", v)
	}
	if rs := byOp[admin.OpRestore]; rs.Target != "staging" || rs.ApprovalID != "ap1" || rs.Snapshot != m.ID {
		t.Fatalf("restore record: %+v", rs)
	}
	list, _ := e.List(context.Background(), "app")
	if len(list) != 1 {
		t.Fatalf("run records must not appear as snapshots: %d", len(list))
	}
}

func TestLock(t *testing.T) {
	e, repoDir, _ := setup(t, false)
	j, _ := e.Cfg.Job("app")
	j.Params["block"] = "true"
	blockCh = make(chan struct{})
	errc := make(chan error, 1)
	go func() { _, err := e.Backup(context.Background(), "app"); errc <- err }()
	<-blockCh

	if _, err := e.Backup(context.Background(), "app"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("second backup: %v", err)
	}
	for i := 0; i < 3; i++ {
		blockCh <- struct{}{}
		if i < 2 {
			<-blockCh
		}
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	stale, _ := json.Marshal(manifest.Lock{Host: "dead", PID: 1, Op: "backup", Expires: time.Now().Add(-time.Hour)})
	os.MkdirAll(filepath.Join(repoDir, "locks"), 0o700)
	os.WriteFile(filepath.Join(repoDir, "locks", "app.lock"), stale, 0o600)
	j.Params["block"] = "false"
	if _, err := e.Backup(context.Background(), "app"); err != nil {
		t.Fatalf("stale lock not removed: %v", err)
	}
}
