package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"cistern/internal/driver"
)

// fakeMySQL mô phỏng mysql/mysqldump (hoặc bản mariadb) và ghi lại mọi lệnh.
type fakeMySQL struct {
	clientVer, serverVer string
	databases            []string
	existing             map[string]bool
	calls                []driver.Command
	optFiles             []string // nội dung file option lúc lệnh chạy
	restored             map[string]string
	failRestore          bool
}

func newFake() *fakeMySQL {
	return &fakeMySQL{
		clientVer: "mysqldump  Ver 8.4.13 for Linux on aarch64 (MySQL Community Server - GPL)",
		serverVer: "8.4.13", databases: []string{"orders", "users"},
		existing: map[string]bool{"orders": true, "users": true}, restored: map[string]string{},
	}
}

func (f *fakeMySQL) run(_ context.Context, c driver.Command) error {
	f.calls = append(f.calls, c)
	if len(c.Args) > 0 && strings.HasPrefix(c.Args[0], "--defaults-extra-file=") {
		p := strings.TrimPrefix(c.Args[0], "--defaults-extra-file=")
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if st.Mode().Perm() != 0o600 {
			return fmt.Errorf("option file mode %v", st.Mode().Perm())
		}
		b, _ := os.ReadFile(p)
		f.optFiles = append(f.optFiles, string(b))
	}
	out := func(s string) error { _, err := io.WriteString(c.Stdout, s); return err }
	var sql, db string
	for _, a := range c.Args {
		if s, ok := strings.CutPrefix(a, "--execute="); ok {
			sql = s
		}
		if s, ok := strings.CutPrefix(a, "--database="); ok {
			db = s
		}
	}
	switch c.Name {
	case "mysqldump", "mariadb-dump":
		if len(c.Args) == 1 && c.Args[0] == "--version" {
			return out(f.clientVer + "\n")
		}
		return out("-- dump of " + c.Args[len(c.Args)-1] + "\n")
	case "mysql", "mariadb":
		switch {
		case sql == "SELECT VERSION()":
			return out(f.serverVer + "\n")
		case strings.HasPrefix(sql, "SELECT schema_name"):
			return out(strings.Join(f.databases, "\n") + "\n")
		case strings.HasPrefix(sql, "SELECT default_character_set_name"):
			return out("utf8mb4\tutf8mb4_0900_ai_ci\n")
		case strings.HasPrefix(sql, "SELECT COUNT(*)"):
			name := strings.TrimSuffix(sql[strings.Index(sql, "'")+1:], "'")
			if f.existing[name] {
				return out("1\n")
			}
			return out("0\n")
		case strings.HasPrefix(sql, "CREATE DATABASE"):
			f.existing[strings.Trim(strings.TrimPrefix(sql, "CREATE DATABASE "), "`")] = true
			return nil
		case strings.HasPrefix(sql, "DROP DATABASE IF EXISTS"):
			delete(f.existing, strings.Trim(strings.TrimPrefix(sql, "DROP DATABASE IF EXISTS "), "`"))
			return nil
		case db != "":
			if f.failRestore {
				return errors.New("ERROR 1064 (42000) at line 1")
			}
			b, _ := io.ReadAll(c.Stdin)
			f.restored[db] = string(b)
			return nil
		}
	}
	return fmt.Errorf("unexpected command %s %v", c.Name, c.Args)
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

func newDriver(t *testing.T, f *fakeMySQL, p driver.Params, have ...string) *MySQL {
	t.Helper()
	m, err := New(p, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(have) == 0 {
		have = []string{"mysqldump", "mysql"}
	}
	m.lookPath = func(s string) (string, error) {
		for _, h := range have {
			if h == s {
				return "/usr/bin/" + s, nil
			}
		}
		return "", errors.New("not found")
	}
	return m
}

func TestBackup(t *testing.T) {
	t.Setenv("CISTERN_TMPDIR", t.TempDir())
	f := newFake()
	m := newDriver(t, f, driver.Params{"host": "db", "port": "3306", "user": "backup", "password": `p"a\ss`})
	sink := &memSink{files: map[string]*bytes.Buffer{}}
	info, err := m.Backup(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.files) != 2 {
		t.Fatalf("artifacts: %v", sink.files)
	}
	got := sink.files["db/orders.sql"].String()
	if !strings.HasPrefix(got, "-- Cistern:") || !strings.Contains(got, "ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n-- dump of orders\n") {
		t.Errorf("orders.sql:\n%s", got)
	}
	if info.Version != "8.4.13" || info.Host != "db:3306" || info.Extra["databases"] != "orders,users" {
		t.Errorf("info: %+v", info)
	}
	for _, c := range f.calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, `p"a`) || strings.Contains(joined, "backup") {
			t.Errorf("credentials in args: %s", c)
		}
		if c.Name == "mysqldump" && len(c.Args) > 1 {
			for _, want := range []string{"--single-transaction", "--routines", "--triggers", "--events", "--hex-blob", "--no-tablespaces"} {
				if !strings.Contains(joined, want) {
					t.Errorf("mysqldump missing %s: %s", want, joined)
				}
			}
		}
	}
	if !strings.Contains(f.optFiles[0], `password="p\"a\\ss"`) || !strings.Contains(f.optFiles[0], `user="backup"`) {
		t.Errorf("option file:\n%s", f.optFiles[0])
	}
	if entries, _ := os.ReadDir(os.Getenv("CISTERN_TMPDIR")); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestBackupVariants(t *testing.T) {
	t.Setenv("CISTERN_TMPDIR", t.TempDir())
	sink := func() *memSink { return &memSink{files: map[string]*bytes.Buffer{}} }

	// MariaDB: chỉ có mariadb-dump/mariadb.
	f := newFake()
	f.clientVer = "mariadb-dump from 11.4.13-MariaDB, client 10.19 for debian-linux-gnu (aarch64)"
	f.serverVer = "11.4.13-MariaDB-ubu2404"
	if _, err := newDriver(t, f, driver.Params{"events": "false"}, "mariadb-dump", "mariadb").Backup(context.Background(), sink()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c.Name == "mariadb-dump" && strings.Contains(strings.Join(c.Args, " "), "--events") {
			t.Error("events=false still dumps events")
		}
	}

	// Client MySQL 8 với server MariaDB cần --column-statistics=0.
	f = newFake()
	f.serverVer = "10.11.9-MariaDB"
	newDriver(t, f, nil).Backup(context.Background(), sink())
	found := false
	for _, c := range f.calls {
		found = found || strings.Contains(strings.Join(c.Args, " "), "--column-statistics=0")
	}
	if !found {
		t.Error("missing --column-statistics=0")
	}

	// Client cũ hơn server cùng họ → từ chối.
	f = newFake()
	f.clientVer = "mysqldump  Ver 8.0.36 for Linux"
	if _, err := newDriver(t, f, nil).Backup(context.Background(), sink()); err == nil || !strings.Contains(err.Error(), "older than server") {
		t.Errorf("old client: %v", err)
	}
	// Tên database không an toàn.
	f = newFake()
	f.databases = []string{"x'; DROP DATABASE y; --"}
	if _, err := newDriver(t, f, nil).Backup(context.Background(), sink()); err == nil {
		t.Error("unsafe database name accepted")
	}
	// Thiếu công cụ.
	if _, err := newDriver(t, newFake(), nil, "psql").Backup(context.Background(), sink()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing tools: %v", err)
	}
	for _, p := range []driver.Params{{"hots": "x"}, {"client": "oracle"}, {"gtid_purged": "MAYBE"}} {
		if _, err := New(p, f.run); err == nil {
			t.Errorf("params %v accepted", p)
		}
	}
	if _, err := newDriver(t, newFake(), driver.Params{"gtid_purged": "OFF"}, "mariadb-dump", "mariadb").Backup(context.Background(), sink()); err == nil {
		t.Error("gtid_purged accepted with mariadb client")
	}
}

func TestRestore(t *testing.T) {
	t.Setenv("CISTERN_TMPDIR", t.TempDir())
	src := memSource{files: map[string]string{"db/orders.sql": "ORDERS", "db/users.sql": "USERS"}}

	f := newFake()
	if err := newDriver(t, f, nil).Restore(context.Background(), src); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected refusal, got %v", err)
	}

	f = newFake()
	if err := newDriver(t, f, driver.Params{"databases": "orders", "restore_suffix": "_restored"}).Restore(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if len(f.restored) != 1 || f.restored["orders_restored"] != "ORDERS" || !f.existing["orders_restored"] {
		t.Fatalf("restored: %v", f.restored)
	}

	f = newFake()
	if err := newDriver(t, f, driver.Params{"clean": "true"}).Restore(context.Background(), src); err != nil || len(f.restored) != 2 {
		t.Fatalf("clean restore: %v %v", err, f.restored)
	}

	// Lỗi khi nạp: database vừa tạo bị xóa; database có sẵn giữ nguyên.
	f = newFake()
	f.failRestore = true
	if err := newDriver(t, f, driver.Params{"restore_suffix": "_new"}).Restore(context.Background(), src); err == nil {
		t.Fatal("expected failure")
	}
	if f.existing["orders_new"] {
		t.Error("partial database not dropped")
	}
	f = newFake()
	f.failRestore = true
	newDriver(t, f, driver.Params{"databases": "orders", "clean": "true"}).Restore(context.Background(), src)
	if !f.existing["orders"] {
		t.Error("pre-existing database dropped")
	}
	if err := newDriver(t, newFake(), driver.Params{"databases": "ghost"}).Restore(context.Background(), src); err == nil {
		t.Error("database not in snapshot accepted")
	}
}
