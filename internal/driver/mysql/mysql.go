// Package mysql là driver "mysql" (FR-04) cho MySQL và MariaDB: mysqldump/mariadb-dump nhất quán
// (--single-transaction) cho từng database, kèm routine, trigger, event; restore bằng client mysql.
//
// Artifact: db/<database>.sql — SQL thuần, khôi phục thủ công được bằng
// `age -d -i key f.sql.gz.age | gunzip | mysql <database>`. Dump không chứa CREATE DATABASE / USE,
// nên restore được sang tên khác; dòng đầu ALTER DATABASE giữ character set và collation gốc.
//
// Tham số (params trong job, ghi đè lúc restore bằng --set):
//
//	host, port, user, password, socket   kết nối; ghi vào file option tạm 0600 (--defaults-extra-file),
//	                                     không qua đối số lệnh hay biến môi trường
//	ssl_mode (chỉ client MySQL), ssl_ca
//	client          auto (mặc định) | mysql | mariadb — bộ công cụ dùng
//	databases       danh sách cách nhau bởi dấu phẩy; trống = mọi database trừ schema hệ thống
//	events          dump event (mặc định true; cần quyền EVENT)
//	gtid_purged     giá trị --set-gtid-purged (OFF | ON | AUTO), chỉ client MySQL
//	restore_suffix  hậu tố tên database đích khi restore
//	clean           cho phép restore vào database đã tồn tại (bảng trong dump bị thay, object khác giữ nguyên)
//
// Hạn chế: view, routine, trigger tham chiếu tên database đầy đủ (`db`.`bảng`) vẫn trỏ về database
// gốc khi restore sang tên khác. Bảng không phải InnoDB không nhất quán với --single-transaction.
// Người dùng và quyền (bảng mysql.*) không được dump.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	dbPrefix = "db/"
	dbSuffix = ".sql"
)

var knownParams = map[string]bool{
	"host": true, "port": true, "user": true, "password": true, "socket": true,
	"ssl_mode": true, "ssl_ca": true, "client": true, "databases": true, "events": true,
	"gtid_purged": true, "restore_suffix": true, "clean": true,
}

var systemSchemas = []string{"mysql", "information_schema", "performance_schema", "sys"}

func init() {
	driver.Register("mysql", func(p driver.Params) (driver.Driver, error) { return New(p, driver.Run) })
}

type MySQL struct {
	p                   driver.Params
	databases           []string
	events, clean       bool
	client, restoreSufx string
	run                 driver.Runner
	lookPath            func(string) (string, error)
}

func New(p driver.Params, run driver.Runner) (*MySQL, error) {
	var unknown []string
	for k := range p {
		if !knownParams[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("mysql: unknown params: %s", strings.Join(unknown, ", "))
	}
	m := &MySQL{p: p, client: p["client"], restoreSufx: p["restore_suffix"], run: run, lookPath: exec.LookPath}
	switch m.client {
	case "":
		m.client = "auto"
	case "auto", "mysql", "mariadb":
	default:
		return nil, fmt.Errorf("mysql: param client must be auto, mysql or mariadb, got %q", m.client)
	}
	for _, d := range strings.Split(p["databases"], ",") {
		if d = strings.TrimSpace(d); d != "" {
			m.databases = append(m.databases, d)
		}
	}
	var err error
	if m.events, err = parseBool(p["events"], true); err != nil {
		return nil, fmt.Errorf("mysql: param events: %w", err)
	}
	if m.clean, err = parseBool(p["clean"], false); err != nil {
		return nil, fmt.Errorf("mysql: param clean: %w", err)
	}
	if g := p["gtid_purged"]; g != "" && g != "OFF" && g != "ON" && g != "AUTO" {
		return nil, fmt.Errorf("mysql: param gtid_purged must be OFF, ON or AUTO, got %q", g)
	}
	return m, nil
}

func parseBool(s string, def bool) (bool, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseBool(s)
}

func (m *MySQL) Name() string { return "mysql" }

// tools: chương trình dump, client và họ (mysql | mariadb).
type tools struct{ dump, cli, flavor string }

func (m *MySQL) tools() (tools, error) {
	have := func(names ...string) bool {
		for _, n := range names {
			if _, err := m.lookPath(n); err != nil {
				return false
			}
		}
		return true
	}
	mysql := tools{"mysqldump", "mysql", "mysql"}
	maria := tools{"mariadb-dump", "mariadb", "mariadb"}
	switch {
	case m.client != "mariadb" && have(mysql.dump, mysql.cli):
		return mysql, nil
	case m.client != "mysql" && have(maria.dump, maria.cli):
		return maria, nil
	}
	return tools{}, fmt.Errorf("mysql: client tools not found in PATH (client=%s: need mysqldump+mysql or mariadb-dump+mariadb)", m.client)
}

func optValue(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// withOptions ghi thông tin kết nối vào file option 0600 trong thư mục tạm 0700 (NFR-02).
func (m *MySQL) withOptions(t tools, f func(optArg string) error) error {
	if t.flavor == "mariadb" && m.p["ssl_mode"] != "" {
		return errors.New("mysql: ssl_mode is only supported with the MySQL client; use ssl_ca")
	}
	var sb strings.Builder
	sb.WriteString("[client]\n")
	for _, kv := range [][2]string{{"user", "user"}, {"password", "password"}, {"host", "host"}, {"port", "port"},
		{"socket", "socket"}, {"ssl_mode", "ssl-mode"}, {"ssl_ca", "ssl-ca"}} {
		if v := m.p[kv[0]]; v != "" {
			sb.WriteString(kv[1] + "=" + optValue(v) + "\n")
		}
	}
	dir, cleanup, err := driver.PrivateDir()
	if err != nil {
		return err
	}
	defer cleanup()
	path, err := driver.WriteSecretFile(dir, "client.cnf", []byte(sb.String()))
	if err != nil {
		return err
	}
	return f("--defaults-extra-file=" + path) // phải là đối số đầu tiên
}

func (m *MySQL) query(ctx context.Context, t tools, opt, sql string) (string, error) {
	return driver.Output(ctx, m.run, driver.Command{Name: t.cli, Args: []string{opt, "--batch", "--skip-column-names", "--execute=" + sql}})
}

var (
	clientVerRe = regexp.MustCompile(`(?:Ver|from) (\d+)\.(\d+)`)
	serverVerRe = regexp.MustCompile(`^(\d+)\.(\d+)`)
)

func majorMinor(re *regexp.Regexp, s string) (int, int, error) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, fmt.Errorf("cannot parse version %q", s)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a, b, nil
}

type versionInfo struct {
	client, server string
	serverMaria    bool
	extraArgs      []string
}

func (m *MySQL) versions(ctx context.Context, t tools, opt string) (versionInfo, error) {
	var v versionInfo
	var err error
	if v.client, err = driver.Output(ctx, m.run, driver.Command{Name: t.dump, Args: []string{"--version"}}); err != nil {
		return v, err
	}
	if v.server, err = m.query(ctx, t, opt, "SELECT VERSION()"); err != nil {
		return v, err
	}
	v.serverMaria = strings.Contains(v.server, "MariaDB")
	cMaj, cMin, err := majorMinor(clientVerRe, v.client)
	if err != nil {
		return v, err
	}
	sMaj, sMin, err := majorMinor(serverVerRe, v.server)
	if err != nil {
		return v, err
	}
	sameFlavor := v.serverMaria == (t.flavor == "mariadb")
	if sameFlavor && (cMaj < sMaj || cMaj == sMaj && cMin < sMin) {
		return v, fmt.Errorf("%s %d.%d is older than server %s: install a client >= %d.%d", t.dump, cMaj, cMin, v.server, sMaj, sMin)
	}
	if t.flavor == "mysql" && cMaj >= 8 && v.serverMaria {
		v.extraArgs = append(v.extraArgs, "--column-statistics=0") // MariaDB không có COLUMN_STATISTICS
	}
	if g := m.p["gtid_purged"]; g != "" {
		if t.flavor != "mysql" {
			return v, errors.New("gtid_purged is only supported with the MySQL client")
		}
		v.extraArgs = append(v.extraArgs, "--set-gtid-purged="+g)
	}
	return v, nil
}

func artifactName(db string) (string, error) {
	name := dbPrefix + db + dbSuffix
	// Tên được nhúng vào SQL ('...' và `...`) và dùng làm đối số: chỉ nhận tập ký tự an toàn.
	if strings.HasPrefix(db, "-") || strings.ContainsAny(db, "'`\"\\") || manifest.ValidateArtifactName(name) != nil {
		return "", fmt.Errorf("database name %q is not supported (allowed: A-Z a-z 0-9 . _ @ = + -, not starting with -)", db)
	}
	return name, nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (m *MySQL) targetDatabases(ctx context.Context, t tools, opt string) ([]string, error) {
	out, err := m.query(ctx, t, opt, "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('"+
		strings.Join(systemSchemas, "','")+"') ORDER BY schema_name")
	if err != nil {
		return nil, err
	}
	all := lines(out)
	if len(m.databases) == 0 {
		return all, nil
	}
	have := map[string]bool{}
	for _, d := range all {
		have[d] = true
	}
	for _, d := range m.databases {
		if !have[d] {
			return nil, fmt.Errorf("database %q does not exist or is not visible to this user", d)
		}
	}
	return m.databases, nil
}

func (m *MySQL) Check(ctx context.Context) error {
	t, err := m.tools()
	if err != nil {
		return err
	}
	return m.withOptions(t, func(opt string) error {
		if _, err := m.versions(ctx, t, opt); err != nil {
			return err
		}
		dbs, err := m.targetDatabases(ctx, t, opt)
		if err != nil {
			return err
		}
		for _, d := range dbs {
			if _, err := artifactName(d); err != nil {
				return err
			}
		}
		return nil
	})
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func (m *MySQL) Backup(ctx context.Context, sink driver.Sink) (driver.Info, error) {
	t, err := m.tools()
	if err != nil {
		return driver.Info{}, err
	}
	var info driver.Info
	err = m.withOptions(t, func(opt string) error {
		v, err := m.versions(ctx, t, opt)
		if err != nil {
			return err
		}
		dbs, err := m.targetDatabases(ctx, t, opt)
		if err != nil {
			return err
		}
		if len(dbs) == 0 {
			return errors.New("no databases to back up")
		}
		for _, db := range dbs {
			if err := m.dumpDB(ctx, t, opt, v, sink, db); err != nil {
				return fmt.Errorf("database %s: %w", db, err)
			}
		}
		host := m.p["host"]
		if host == "" {
			host = "localhost"
		}
		if port := m.p["port"]; port != "" {
			host += ":" + port
		}
		info = driver.Info{Version: v.server, Host: host, Extra: map[string]string{
			"client": t.dump + " " + v.client, "databases": strings.Join(dbs, ","),
		}}
		return nil
	})
	return info, err
}

func (m *MySQL) dumpDB(ctx context.Context, t tools, opt string, v versionInfo, sink driver.Sink, db string) error {
	name, err := artifactName(db)
	if err != nil {
		return err
	}
	cs, err := m.query(ctx, t, opt, "SELECT default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name = '"+db+"'")
	if err != nil {
		return err
	}
	f := strings.Fields(cs)
	if len(f) != 2 || !identRe.MatchString(f[0]) || !identRe.MatchString(f[1]) {
		return fmt.Errorf("unexpected character set %q", cs)
	}
	w, err := sink.Create(name)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("-- Cistern: character set and collation of database %s\nALTER DATABASE CHARACTER SET %s COLLATE %s;\n", db, f[0], f[1])
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	args := []string{opt, "--single-transaction", "--quick", "--routines", "--triggers", "--hex-blob",
		"--no-tablespaces", "--default-character-set=utf8mb4"}
	if m.events {
		args = append(args, "--events")
	}
	args = append(args, v.extraArgs...)
	args = append(args, db)
	if err := m.run(ctx, driver.Command{Name: t.dump, Args: args, Stdout: w}); err != nil {
		return err // engine hủy artifact đang mở
	}
	return w.Close()
}

func (m *MySQL) Restore(ctx context.Context, src driver.Source) error {
	inSnapshot := map[string]bool{}
	var dbs []string
	for _, name := range src.List() {
		if strings.HasPrefix(name, dbPrefix) && strings.HasSuffix(name, dbSuffix) {
			db := strings.TrimSuffix(strings.TrimPrefix(name, dbPrefix), dbSuffix)
			inSnapshot[db] = true
			dbs = append(dbs, db)
		}
	}
	if len(m.databases) > 0 {
		var missing []string
		for _, d := range m.databases {
			if !inSnapshot[d] {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("databases not in snapshot: %s", strings.Join(missing, ", "))
		}
		dbs = m.databases
	}
	if len(dbs) == 0 {
		return errors.New("nothing to restore: snapshot has no database artifacts")
	}
	t, err := m.tools()
	if err != nil {
		return err
	}
	return m.withOptions(t, func(opt string) error {
		for _, db := range dbs {
			target := db + m.restoreSufx
			if err := m.restoreDB(ctx, t, opt, src, dbPrefix+db+dbSuffix, target); err != nil {
				return fmt.Errorf("database %s → %s: %w", db, target, err)
			}
		}
		return nil
	})
}

func (m *MySQL) restoreDB(ctx context.Context, t tools, opt string, src driver.Source, artifact, target string) error {
	if _, err := artifactName(target); err != nil {
		return err
	}
	exists, err := m.query(ctx, t, opt, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '"+target+"'")
	if err != nil {
		return err
	}
	created := false
	if exists != "0" {
		if !m.clean {
			return fmt.Errorf("database %q already exists: restore under another name (--set restore_suffix=_restored) or into it with --set clean=true", target)
		}
	} else {
		if _, err := m.query(ctx, t, opt, "CREATE DATABASE `"+target+"`"); err != nil {
			return err
		}
		created = true
	}
	rc, err := src.Open(artifact)
	if err == nil {
		err = m.run(ctx, driver.Command{Name: t.cli, Args: []string{opt, "--database=" + target}, Stdin: rc})
		if err != nil {
			driver.Abort(rc)
		} else {
			err = rc.Close() // đọc nốt và kiểm checksum (NFR-03)
		}
	}
	if err != nil && created {
		// Database do chính lần restore này tạo: xóa bản dở để lần thử lại không bị "already exists".
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if _, derr := m.query(dctx, t, opt, "DROP DATABASE IF EXISTS `"+target+"`"); derr != nil {
			return fmt.Errorf("%w (dropping partial database %q also failed: %v)", err, target, derr)
		}
	}
	return err
}
