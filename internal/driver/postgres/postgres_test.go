package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"cistern/internal/driver"
)

// fakePG mô phỏng psql/pg_dump/pg_dumpall/pg_restore và ghi lại mọi lệnh.
type fakePG struct {
	client, server string
	databases      []string
	existing       map[string]bool
	calls          []driver.Command
	restored       map[string]string // database → nội dung pg_restore nhận
	failDump       string
	failRestore    bool
}

func newFake() *fakePG {
	return &fakePG{
		client: "16.4", server: "16.2",
		databases: []string{"orders", "users"},
		existing:  map[string]bool{"orders": true, "users": true, "postgres": true},
		restored:  map[string]string{},
	}
}

func (f *fakePG) run(_ context.Context, c driver.Command) error {
	f.calls = append(f.calls, c)
	var stdin string
	if c.Stdin != nil {
		b, err := io.ReadAll(c.Stdin)
		if err != nil {
			return err
		}
		stdin = string(b)
	}
	vars := map[string]string{}
	var dbname string
	for i, a := range c.Args {
		if a == "-v" && i+1 < len(c.Args) {
			k, v, _ := strings.Cut(c.Args[i+1], "=")
			vars[k] = v
		}
		if strings.HasPrefix(a, "--dbname=") {
			dbname = strings.TrimPrefix(a, "--dbname=")
		}
	}
	out := func(s string) error { _, err := io.WriteString(c.Stdout, s); return err }
	switch c.Name {
	case "pg_dump":
		if len(c.Args) == 1 && c.Args[0] == "--version" {
			return out("pg_dump (PostgreSQL) " + f.client + "\n")
		}
		if dbname == f.failDump {
			return errors.New("pg_dump: connection refused")
		}
		return out("DUMP:" + dbname)
	case "pg_dumpall":
		return out("GLOBALS")
	case "pg_restore":
		if f.failRestore {
			return errors.New("pg_restore: corrupt archive")
		}
		f.restored[dbname] = stdin
		return nil
	case "psql":
		switch {
		case strings.Contains(stdin, "SHOW server_version"):
			return out(f.server + "\n")
		case strings.Contains(stdin, "SELECT datname"):
			return out(strings.Join(f.databases, "\n") + "\n")
		case strings.Contains(stdin, "count(*) FROM pg_database"):
			if f.existing[vars["name"]] {
				return out("1\n")
			}
			return out("0\n")
		case strings.Contains(stdin, "CREATE DATABASE"):
			f.existing[vars["name"]] = true
			return nil
		case strings.Contains(stdin, "DROP DATABASE"):
			delete(f.existing, vars["name"])
			return nil
		}
		return nil
	}
	return fmt.Errorf("unexpected command %s", c.Name)
}

type memSink struct{ files map[string]*bytes.Buffer }

type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

func (s *memSink) Create(name string) (io.WriteCloser, error) {
	b := &bytes.Buffer{}
	s.files[name] = b
	return nopCloser{b}, nil
}
func (s *memSink) Previous() driver.Source { return nil }

type memSource struct{ files map[string]string }

func (s memSource) List() []string {
	var ks []string
	for k := range s.files {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func (s memSource) Open(name string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.files[name])), nil
}

func newPG(t *testing.T, f *fakePG, p driver.Params) *Postgres {
	t.Helper()
	pg, err := New(p, f.run)
	if err != nil {
		t.Fatal(err)
	}
	pg.lookPath = func(s string) (string, error) { return "/usr/bin/" + s, nil }
	return pg
}

func TestBackup(t *testing.T) {
	f := newFake()
	pg := newPG(t, f, driver.Params{"host": "db", "port": "5432", "user": "backup", "password": "s3cret"})
	sink := &memSink{files: map[string]*bytes.Buffer{}}
	info, err := pg.Backup(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"db/orders.dump": "DUMP:orders", "db/users.dump": "DUMP:users", "globals.sql": "GLOBALS"}
	if len(sink.files) != len(want) {
		t.Fatalf("artifacts: %v", sink.files)
	}
	for k, v := range want {
		if sink.files[k] == nil || sink.files[k].String() != v {
			t.Errorf("%s = %v", k, sink.files[k])
		}
	}
	if info.Version != "16.2" || info.Host != "db:5432" || info.Extra["pg_dump_version"] != "16.4" || info.Extra["databases"] != "orders,users" {
		t.Errorf("info: %+v", info)
	}
	// NFR-02: mật khẩu chỉ nằm trong env, không bao giờ trong đối số.
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.Args, " "), "s3cret") {
			t.Errorf("password in args: %s", c)
		}
		if c.Name != "pg_dump" || len(c.Args) > 1 {
			found := false
			for _, e := range c.Env {
				found = found || e == "PGPASSWORD=s3cret"
			}
			if !found {
				t.Errorf("PGPASSWORD missing for %s", c)
			}
		}
	}
}

func TestBackupSelectedAndErrors(t *testing.T) {
	f := newFake()
	pg := newPG(t, f, driver.Params{"databases": "users", "globals": "false"})
	sink := &memSink{files: map[string]*bytes.Buffer{}}
	if _, err := pg.Backup(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.files) != 1 || sink.files["db/users.dump"] == nil {
		t.Fatalf("artifacts: %v", sink.files)
	}

	if _, err := newPG(t, newFake(), driver.Params{"databases": "nope"}).Backup(context.Background(), sink); err == nil {
		t.Error("missing database accepted")
	}
	old := newFake()
	old.client = "14.9"
	if _, err := newPG(t, old, nil).Backup(context.Background(), sink); err == nil || !strings.Contains(err.Error(), "older than server") {
		t.Errorf("old client: %v", err)
	}
	bad := newFake()
	bad.databases = []string{"a=b"}
	if _, err := newPG(t, bad, nil).Backup(context.Background(), sink); err == nil {
		t.Error("db name with = accepted")
	}
	fail := newFake()
	fail.failDump = "users"
	if _, err := newPG(t, fail, nil).Backup(context.Background(), sink); err == nil || !strings.Contains(err.Error(), "users") {
		t.Errorf("dump failure: %v", err)
	}
	if _, err := New(driver.Params{"hots": "x"}, f.run); err == nil {
		t.Error("unknown param accepted")
	}
}

func TestRestore(t *testing.T) {
	src := memSource{files: map[string]string{"db/orders.dump": "DUMP:orders", "db/users.dump": "DUMP:users", "globals.sql": "GLOBALS"}}

	// Đích đã tồn tại, không clean → từ chối.
	f := newFake()
	if err := newPG(t, f, nil).Restore(context.Background(), src); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected refusal, got %v", err)
	}

	// Restore sang tên mới một database.
	f = newFake()
	pg := newPG(t, f, driver.Params{"databases": "orders", "restore_suffix": "_restored"})
	if err := pg.Restore(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if f.restored["orders_restored"] != "DUMP:orders" || len(f.restored) != 1 || !f.existing["orders_restored"] {
		t.Fatalf("restored: %v", f.restored)
	}

	// clean=true ghi đè database có sẵn với --clean --if-exists.
	f = newFake()
	if err := newPG(t, f, driver.Params{"clean": "true"}).Restore(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if len(f.restored) != 2 {
		t.Fatalf("restored: %v", f.restored)
	}
	for _, c := range f.calls {
		if c.Name == "pg_restore" && !strings.Contains(strings.Join(c.Args, " "), "--clean --if-exists") {
			t.Errorf("missing --clean: %s", c)
		}
	}

	if err := newPG(t, newFake(), driver.Params{"databases": "ghost"}).Restore(context.Background(), src); err == nil {
		t.Error("database not in snapshot accepted")
	}

	// pg_restore lỗi: database vừa tạo bị xóa; database có sẵn (clean=true) thì giữ nguyên.
	f = newFake()
	f.failRestore = true
	if err := newPG(t, f, driver.Params{"databases": "orders", "restore_suffix": "_new"}).Restore(context.Background(), src); err == nil {
		t.Fatal("expected failure")
	}
	if f.existing["orders_new"] {
		t.Error("partial database not dropped")
	}
	f = newFake()
	f.failRestore = true
	newPG(t, f, driver.Params{"databases": "orders", "clean": "true"}).Restore(context.Background(), src)
	if !f.existing["orders"] {
		t.Error("pre-existing database dropped")
	}
}
