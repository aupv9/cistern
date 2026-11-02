// Package postgres là driver "postgres" (FR-03): pg_dump định dạng custom cho từng database,
// pg_dumpall --globals-only cho role và tablespace; restore bằng pg_restore.
//
// Artifact:
//
//	db/<database>.dump   pg_dump --format=custom
//	globals.sql          pg_dumpall --globals-only (khi globals=true)
//
// Tham số (params trong job, ghi đè lúc restore bằng --set):
//
//	host, port, user, password, sslmode   kết nối (truyền qua biến PG*, không qua đối số lệnh)
//	maintenance_db     database dùng để liệt kê/tạo database (mặc định postgres)
//	databases          danh sách cách nhau bởi dấu phẩy; trống = mọi database cho phép kết nối
//	globals            dump role/tablespace (mặc định true)
//	no_role_passwords  thêm --no-role-passwords cho pg_dumpall (cho dịch vụ managed)
//	restore_suffix     hậu tố tên database đích khi restore (vd. _restored)
//	clean              cho phép restore đè database đã tồn tại (--clean --if-exists)
//	no_owner           pg_restore --no-owner --no-privileges
//	restore_globals    chạy globals.sql khi restore (mặc định false)
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cistern/internal/driver"
	"cistern/internal/manifest"
)

const (
	globalsArtifact = "globals.sql"
	dbPrefix        = "db/"
	dbSuffix        = ".dump"
)

var knownParams = map[string]bool{
	"host": true, "port": true, "user": true, "password": true, "sslmode": true,
	"maintenance_db": true, "databases": true, "globals": true, "no_role_passwords": true,
	"restore_suffix": true, "clean": true, "no_owner": true, "restore_globals": true,
}

func init() {
	driver.Register("postgres", func(p driver.Params) (driver.Driver, error) { return New(p, driver.Run) })
}

type Postgres struct {
	host, port, user, password, sslmode string
	maintDB                             string
	databases                           []string
	globals, noRolePasswords            bool
	restoreSuffix                       string
	clean, noOwner, restoreGlobals      bool
	run                                 driver.Runner
	lookPath                            func(string) (string, error)
}

func New(p driver.Params, run driver.Runner) (*Postgres, error) {
	var unknown []string
	for k := range p {
		if !knownParams[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("postgres: unknown params: %s", strings.Join(unknown, ", "))
	}
	pg := &Postgres{
		host: p["host"], port: p["port"], user: p["user"], password: p["password"], sslmode: p["sslmode"],
		maintDB:       p["maintenance_db"],
		restoreSuffix: p["restore_suffix"],
		run:           run,
		lookPath:      exec.LookPath,
	}
	if pg.maintDB == "" {
		pg.maintDB = "postgres"
	}
	for _, d := range strings.Split(p["databases"], ",") {
		if d = strings.TrimSpace(d); d != "" {
			pg.databases = append(pg.databases, d)
		}
	}
	var err error
	bools := []struct {
		key string
		def bool
		dst *bool
	}{
		{"globals", true, &pg.globals},
		{"no_role_passwords", false, &pg.noRolePasswords},
		{"clean", false, &pg.clean},
		{"no_owner", false, &pg.noOwner},
		{"restore_globals", false, &pg.restoreGlobals},
	}
	for _, b := range bools {
		if *b.dst, err = parseBool(p[b.key], b.def); err != nil {
			return nil, fmt.Errorf("postgres: param %s: %w", b.key, err)
		}
	}
	return pg, nil
}

func parseBool(s string, def bool) (bool, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseBool(s)
}

func (pg *Postgres) Name() string { return "postgres" }

// env: thông tin kết nối qua biến môi trường libpq, giữ mật khẩu ngoài dòng lệnh (NFR-02).
func (pg *Postgres) env() []string {
	e := []string{"PGAPPNAME=cistern", "PGCONNECT_TIMEOUT=15"}
	for k, v := range map[string]string{"PGHOST": pg.host, "PGPORT": pg.port, "PGUSER": pg.user, "PGPASSWORD": pg.password, "PGSSLMODE": pg.sslmode} {
		if v != "" {
			e = append(e, k+"="+v)
		}
	}
	return e
}

func (pg *Postgres) hostLabel() string {
	h := pg.host
	if h == "" {
		h = "localhost"
	}
	if pg.port != "" {
		h += ":" + pg.port
	}
	return h
}

// psql chạy một script qua stdin để dùng biến :'name' / :"name" (psql tự quote, tránh SQL injection).
func (pg *Postgres) psql(ctx context.Context, db, script string, vars map[string]string) (string, error) {
	args := []string{"-X", "-q", "-At", "-v", "ON_ERROR_STOP=1", "--no-password", "-d", db}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-v", k+"="+vars[k])
	}
	return driver.Output(ctx, pg.run, driver.Command{Name: "psql", Args: args, Env: pg.env(), Stdin: strings.NewReader(script)})
}

func majorOf(s string) (int, error) {
	m := regexp.MustCompile(`(\d+)(?:\.(\d+))?`).FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("cannot parse version %q", s)
	}
	return strconv.Atoi(m[1])
}

func (pg *Postgres) versions(ctx context.Context) (client, server string, err error) {
	out, err := driver.Output(ctx, pg.run, driver.Command{Name: "pg_dump", Args: []string{"--version"}})
	if err != nil {
		return "", "", err
	}
	client = strings.TrimSpace(strings.TrimPrefix(out, "pg_dump (PostgreSQL)"))
	server, err = pg.psql(ctx, pg.maintDB, "SHOW server_version;\n", nil)
	if err != nil {
		return "", "", err
	}
	cm, err := majorOf(client)
	if err != nil {
		return "", "", err
	}
	sm, err := majorOf(server)
	if err != nil {
		return "", "", err
	}
	// pg_dump cũ hơn server sẽ lỗi; dump mới có thể không restore lên bản cũ → ghi cả hai vào manifest.
	if cm < sm {
		return "", "", fmt.Errorf("pg_dump %s is older than server %s: install a client with major version >= %d", client, server, sm)
	}
	return client, server, nil
}

func (pg *Postgres) listDatabases(ctx context.Context) ([]string, error) {
	out, err := pg.psql(ctx, pg.maintDB, "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1;\n", nil)
	if err != nil {
		return nil, err
	}
	var dbs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dbs = append(dbs, l)
		}
	}
	return dbs, nil
}

func (pg *Postgres) targetDatabases(ctx context.Context) ([]string, error) {
	all, err := pg.listDatabases(ctx)
	if err != nil {
		return nil, err
	}
	if len(pg.databases) == 0 {
		return all, nil
	}
	have := map[string]bool{}
	for _, d := range all {
		have[d] = true
	}
	for _, d := range pg.databases {
		if !have[d] {
			return nil, fmt.Errorf("database %q does not exist or does not allow connections", d)
		}
	}
	return pg.databases, nil
}

func artifactName(db string) (string, error) {
	// Tên database dùng làm tên artifact và truyền cho -d; "=" khiến libpq hiểu là chuỗi kết nối.
	name := dbPrefix + db + dbSuffix
	if strings.Contains(db, "=") || manifest.ValidateArtifactName(name) != nil {
		return "", fmt.Errorf("database name %q is not supported (allowed: A-Z a-z 0-9 . _ @ + -)", db)
	}
	return name, nil
}

func (pg *Postgres) Check(ctx context.Context) error {
	for _, bin := range []string{"pg_dump", "pg_dumpall", "pg_restore", "psql"} {
		if _, err := pg.lookPath(bin); err != nil {
			return fmt.Errorf("%s not found in PATH", bin)
		}
	}
	if _, _, err := pg.versions(ctx); err != nil {
		return err
	}
	dbs, err := pg.targetDatabases(ctx)
	if err != nil {
		return err
	}
	for _, d := range dbs {
		if _, err := artifactName(d); err != nil {
			return err
		}
	}
	return nil
}

func (pg *Postgres) Backup(ctx context.Context, sink driver.Sink) (driver.Info, error) {
	client, server, err := pg.versions(ctx)
	if err != nil {
		return driver.Info{}, err
	}
	dbs, err := pg.targetDatabases(ctx)
	if err != nil {
		return driver.Info{}, err
	}
	if len(dbs) == 0 {
		return driver.Info{}, errors.New("no databases to back up")
	}
	for _, db := range dbs {
		name, err := artifactName(db)
		if err != nil {
			return driver.Info{}, err
		}
		if err := pg.dumpTo(ctx, sink, name, driver.Command{
			Name: "pg_dump",
			Args: []string{"--format=custom", "--no-password", "--dbname=" + db},
		}); err != nil {
			return driver.Info{}, fmt.Errorf("database %s: %w", db, err)
		}
	}
	if pg.globals {
		args := []string{"--globals-only", "--no-password", "--database=" + pg.maintDB}
		if pg.noRolePasswords {
			args = append(args, "--no-role-passwords")
		}
		if err := pg.dumpTo(ctx, sink, globalsArtifact, driver.Command{Name: "pg_dumpall", Args: args}); err != nil {
			return driver.Info{}, fmt.Errorf("globals: %w", err)
		}
	}
	return driver.Info{
		Version: server,
		Host:    pg.hostLabel(),
		Extra:   map[string]string{"pg_dump_version": client, "databases": strings.Join(dbs, ",")},
	}, nil
}

func (pg *Postgres) dumpTo(ctx context.Context, sink driver.Sink, name string, c driver.Command) error {
	w, err := sink.Create(name)
	if err != nil {
		return err
	}
	c.Env = pg.env()
	c.Stdout = w
	if err := pg.run(ctx, c); err != nil {
		return err // engine hủy artifact đang mở
	}
	return w.Close()
}

func (pg *Postgres) Restore(ctx context.Context, src driver.Source) error {
	inSnapshot := map[string]bool{}
	var dbs []string
	hasGlobals := false
	for _, name := range src.List() {
		if name == globalsArtifact {
			hasGlobals = true
			continue
		}
		if strings.HasPrefix(name, dbPrefix) && strings.HasSuffix(name, dbSuffix) {
			db := strings.TrimSuffix(strings.TrimPrefix(name, dbPrefix), dbSuffix)
			inSnapshot[db] = true
			dbs = append(dbs, db)
		}
	}
	if len(pg.databases) > 0 {
		var missing []string
		for _, d := range pg.databases {
			if !inSnapshot[d] {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("databases not in snapshot: %s", strings.Join(missing, ", "))
		}
		dbs = pg.databases
	}
	if len(dbs) == 0 && !(pg.restoreGlobals && hasGlobals) {
		return errors.New("nothing to restore: snapshot has no matching database artifacts")
	}

	if pg.restoreGlobals {
		if !hasGlobals {
			return errors.New("restore_globals=true but snapshot has no globals.sql")
		}
		if err := pg.restoreGlobalsSQL(ctx, src); err != nil {
			return fmt.Errorf("globals: %w", err)
		}
	}
	for _, db := range dbs {
		target := db + pg.restoreSuffix
		if err := pg.restoreDB(ctx, src, dbPrefix+db+dbSuffix, target); err != nil {
			return fmt.Errorf("database %s → %s: %w", db, target, err)
		}
	}
	return nil
}

func (pg *Postgres) restoreGlobalsSQL(ctx context.Context, src driver.Source) error {
	rc, err := src.Open(globalsArtifact)
	if err != nil {
		return err
	}
	err = pg.run(ctx, driver.Command{
		Name:  "psql",
		Args:  []string{"-X", "-q", "-v", "ON_ERROR_STOP=1", "--no-password", "-d", pg.maintDB},
		Env:   pg.env(),
		Stdin: rc,
	})
	if err != nil {
		driver.Abort(rc)
		return err
	}
	return rc.Close()
}

func (pg *Postgres) restoreDB(ctx context.Context, src driver.Source, artifact, target string) error {
	if _, err := artifactName(target); err != nil {
		return err
	}
	exists, err := pg.psql(ctx, pg.maintDB, "SELECT count(*) FROM pg_database WHERE datname = :'name';\n", map[string]string{"name": target})
	if err != nil {
		return err
	}
	created := false
	if exists != "0" {
		if !pg.clean {
			return fmt.Errorf("database %q already exists: restore under another name (--set restore_suffix=_restored) or overwrite with --set clean=true", target)
		}
	} else {
		if _, err := pg.psql(ctx, pg.maintDB, "CREATE DATABASE :\"name\";\n", map[string]string{"name": target}); err != nil {
			return err
		}
		created = true
	}
	if err := pg.pgRestore(ctx, src, artifact, target); err != nil {
		if created {
			// Database do chính lần restore này tạo: xóa bản dở dang để lần thử lại không bị "already exists".
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if _, derr := pg.psql(dctx, pg.maintDB, "DROP DATABASE IF EXISTS :\"name\";\n", map[string]string{"name": target}); derr != nil {
				return fmt.Errorf("%w (dropping partial database %q also failed: %v)", err, target, derr)
			}
		}
		return err
	}
	return nil
}

func (pg *Postgres) pgRestore(ctx context.Context, src driver.Source, artifact, target string) error {

	rc, err := src.Open(artifact)
	if err != nil {
		return err
	}
	args := []string{"--no-password", "--exit-on-error", "--dbname=" + target}
	if pg.clean {
		args = append(args, "--clean", "--if-exists")
	}
	if pg.noOwner {
		args = append(args, "--no-owner", "--no-privileges")
	}
	if err := pg.run(ctx, driver.Command{Name: "pg_restore", Args: args, Env: pg.env(), Stdin: rc}); err != nil {
		driver.Abort(rc)
		return err
	}
	// Close đọc nốt phần còn lại và kiểm checksum (NFR-03).
	return rc.Close()
}
