package mysql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cistern/internal/admin"
	"cistern/internal/driver"
)

const markerTable = "_cistern_drill"

type drillDB struct {
	source, target string
	stale, created bool
}

type markerState int

const (
	markerAbsent markerState = iota
	markerOurs
	markerForeign
)

func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}

func ident(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func unescapeBatch(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\\`, `\`, `\t`, "\t", `\n`, "\n", `\0`, "\x00").Replace(s)
}

func (m *MySQL) script(ctx context.Context, t tools, opt, db, sql string) (string, error) {
	args := []string{opt, "--batch", "--skip-column-names"}
	if db != "" {
		args = append(args, "--database="+db)
	}
	return driver.Output(ctx, m.run, driver.Command{Name: t.cli, Args: args, Stdin: strings.NewReader(sql)})
}

func (m *MySQL) markerOf(ctx context.Context, t tools, opt, name, job string) (markerState, error) {
	n, err := m.query(ctx, t, opt, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = "+sqlString(name))
	if err != nil {
		return markerAbsent, err
	}
	if n == "0" {
		return markerAbsent, nil
	}
	n, err = m.query(ctx, t, opt, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = "+sqlString(name)+" AND table_name = '"+markerTable+"'")
	if err != nil {
		return markerAbsent, err
	}
	if n == "0" {
		return markerForeign, nil
	}
	owner, err := m.query(ctx, t, opt, "SELECT job FROM "+ident(name)+"."+markerTable+" LIMIT 1")
	if err != nil {
		return markerAbsent, err
	}
	if owner == job {
		return markerOurs, nil
	}
	return markerForeign, nil
}

func (m *MySQL) dropDrillDB(ctx context.Context, t tools, opt, name, job string) error {
	st, err := m.markerOf(ctx, t, opt, name, job)
	if err != nil {
		return err
	}
	switch st {
	case markerAbsent:
		return nil
	case markerForeign:
		return fmt.Errorf("database %q lost its drill marker; not dropping it", name)
	}
	_, err = m.query(ctx, t, opt, "DROP DATABASE "+ident(name))
	return err
}

func (m *MySQL) Drill(ctx context.Context, src driver.Source, spec driver.DrillSpec) (*admin.DrillReport, error) {
	rep := &admin.DrillReport{Checks: []admin.DrillCheck{}}
	dbs, err := m.selectDatabases(src)
	if err != nil {
		return rep, err
	}
	if len(dbs) == 0 {
		return rep, errors.New("nothing to drill: the snapshot has no database artifacts")
	}
	if spec.Suffix == "" {
		return rep, errors.New("a drill needs a restore suffix")
	}
	t, err := m.tools()
	if err != nil {
		return rep, err
	}
	err = m.withOptions(t, func(opt string) error { return m.drill(ctx, t, opt, src, spec, dbs, rep) })
	return rep, err
}

func (m *MySQL) drill(ctx context.Context, t tools, opt string, src driver.Source, spec driver.DrillSpec, dbs []string, rep *admin.DrillReport) error {
	var targets []*drillDB
	for _, db := range dbs {
		d := &drillDB{source: db, target: db + spec.Suffix}
		if _, err := artifactName(d.target); err != nil {
			return err
		}
		st, err := m.markerOf(ctx, t, opt, d.target, spec.Job)
		if err != nil {
			return err
		}
		if st == markerForeign {
			rep.CleanedUp = true
			rep.Add(admin.DrillCheck{Name: "restore " + db, Kind: admin.KindRestore, Status: admin.CheckFailed,
				Message: fmt.Sprintf("database %q already exists and has no drill marker of job %s; it was left untouched", d.target, spec.Job)})
			return nil
		}
		d.stale = st == markerOurs
		targets = append(targets, d)
	}

	var restored atomic.Int64
	ok := true
	for _, d := range targets {
		if !ok {
			for _, k := range []string{admin.KindRestore, admin.KindTables, admin.KindRows} {
				rep.Add(admin.DrillCheck{Name: k + " " + d.source, Kind: k, Status: admin.CheckSkipped, Message: "an earlier database failed"})
			}
			continue
		}
		ok = m.drillDatabase(ctx, t, opt, src, d, spec.Job, &restored, rep)
	}
	rep.RestoredBytes = restored.Load()

	for _, c := range spec.Checks {
		if !ok {
			rep.Add(admin.DrillCheck{Name: c.Name, Kind: admin.KindSQL, Status: admin.CheckSkipped, Expected: c.Expectation(), Message: "the restore failed"})
			continue
		}
		rep.Add(m.sqlCheck(ctx, t, opt, c, targets, spec.Suffix))
	}

	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	m.cleanup(cctx, t, opt, targets, spec.Job, spec.Keep, rep)
	return nil
}

func (m *MySQL) drillDatabase(ctx context.Context, t tools, opt string, src driver.Source, d *drillDB, job string, restored *atomic.Int64, rep *admin.DrillReport) bool {
	fail := func(kind, msg string) bool {
		rep.Add(admin.DrillCheck{Name: kind + " " + d.source, Kind: kind, Status: admin.CheckFailed, Message: msg})
		if kind == admin.KindRestore {
			for _, k := range []string{admin.KindTables, admin.KindRows} {
				rep.Add(admin.DrillCheck{Name: k + " " + d.source, Kind: k, Status: admin.CheckSkipped, Message: "the restore failed"})
			}
		}
		return false
	}
	if d.stale {
		if err := m.dropDrillDB(ctx, t, opt, d.target, job); err != nil {
			return fail(admin.KindRestore, "dropping the previous drill database "+d.target+": "+err.Error())
		}
	}
	if _, err := m.query(ctx, t, opt, "CREATE DATABASE "+ident(d.target)); err != nil {
		return fail(admin.KindRestore, err.Error())
	}
	d.created = true
	mark := "CREATE TABLE " + ident(d.target) + "." + markerTable + " (job VARCHAR(255) NOT NULL, created_at DATETIME NOT NULL); " +
		"INSERT INTO " + ident(d.target) + "." + markerTable + " VALUES (" + sqlString(job) + ", NOW())"
	if _, err := m.query(ctx, t, opt, mark); err != nil {
		return fail(admin.KindRestore, "marking the drill database: "+err.Error())
	}

	rc, err := src.Open(dbPrefix + d.source + dbSuffix)
	if err != nil {
		return fail(admin.KindRestore, err.Error())
	}
	creates := &driver.PrefixCounter{Prefix: "CREATE TABLE "}
	before := restored.Load()
	in := io.TeeReader(driver.CountingReader{R: rc, N: restored}, creates)
	if err := m.run(ctx, driver.Command{Name: t.cli, Args: []string{opt, "--database=" + d.target}, Stdin: in}); err != nil {
		driver.Abort(rc)
		return fail(admin.KindRestore, err.Error())
	}
	if err := rc.Close(); err != nil {
		return fail(admin.KindRestore, err.Error())
	}
	rep.Add(admin.DrillCheck{Name: admin.KindRestore + " " + d.source, Kind: admin.KindRestore, Status: admin.CheckPassed,
		Actual: strconv.FormatInt(restored.Load()-before, 10) + " bytes", Message: t.cli + " loaded the dump into " + d.target})

	out, err := m.query(ctx, t, opt, "SELECT table_name FROM information_schema.tables WHERE table_schema = "+sqlString(d.target)+
		" AND table_type = 'BASE TABLE' AND table_name <> '"+markerTable+"' ORDER BY table_name")
	if err != nil {
		return fail(admin.KindTables, err.Error())
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			names = append(names, unescapeBatch(l))
		}
	}
	chk := admin.DrillCheck{Name: admin.KindTables + " " + d.source, Kind: admin.KindTables, Expected: strconv.Itoa(creates.Count), Actual: strconv.Itoa(len(names)), Status: admin.CheckPassed}
	if len(names) != creates.Count {
		chk.Status, chk.Message = admin.CheckFailed, "the restored database has a different number of tables than CREATE TABLE statements in the dump"
	}
	rep.Add(chk)
	if chk.Status == admin.CheckFailed {
		return false
	}

	tables, err := m.rowCounts(ctx, t, opt, d, names)
	if err != nil {
		return fail(admin.KindRows, err.Error())
	}
	var total int64
	for _, tb := range tables {
		total += tb.Rows
	}
	rep.AddTables(tables)
	rep.Add(admin.DrillCheck{Name: admin.KindRows + " " + d.source, Kind: admin.KindRows, Status: admin.CheckPassed,
		Actual: fmt.Sprintf("%d rows in %d tables", total, len(tables))})
	return true
}

func (m *MySQL) rowCounts(ctx context.Context, t tools, opt string, d *drillDB, names []string) ([]admin.DrillTable, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("SELECT COUNT(*) FROM " + ident(d.target) + "." + ident(n) + ";\n")
	}
	out, err := m.script(ctx, t, opt, "", sb.String())
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != len(names) {
		return nil, fmt.Errorf("expected %d row counts, got %d lines", len(names), len(lines))
	}
	tables := make([]admin.DrillTable, len(names))
	for i, n := range names {
		rows, err := strconv.ParseInt(strings.TrimSpace(lines[i]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected row count %q for %s", lines[i], n)
		}
		tables[i] = admin.DrillTable{Database: d.source, Name: n, Rows: rows}
	}
	return tables, nil
}

func (m *MySQL) sqlCheck(ctx context.Context, t tools, opt string, c driver.DrillSQL, targets []*drillDB, suffix string) admin.DrillCheck {
	db := c.Database
	if db == "" {
		if len(targets) != 1 {
			names := make([]string, len(targets))
			for i, d := range targets {
				names[i] = d.source
			}
			return driver.SQLCheck(c, "", fmt.Errorf("set database: the snapshot has several databases (%s)", strings.Join(names, ", ")), "\t")
		}
		db = targets[0].source
	}
	found := false
	for _, d := range targets {
		found = found || d.source == db
	}
	if !found {
		return driver.SQLCheck(c, "", fmt.Errorf("database %q is not in the snapshot", db), "\t")
	}
	out, err := m.script(ctx, t, opt, db+suffix, "START TRANSACTION READ ONLY;\n"+driver.TrimSQL(c.SQL)+";\nCOMMIT;\n")
	return driver.SQLCheck(c, out, err, "\t")
}

func (m *MySQL) cleanup(ctx context.Context, t tools, opt string, targets []*drillDB, job string, keep bool, rep *admin.DrillReport) {
	var created []string
	for _, d := range targets {
		if d.created {
			created = append(created, d.target)
		}
	}
	sort.Strings(created)
	if keep {
		rep.CleanedUp = len(created) == 0
		rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckSkipped,
			Message: "keep: true, the drill databases stay until the next drill: " + strings.Join(created, ", ")})
		return
	}
	var failed []string
	for _, name := range created {
		if err := m.dropDrillDB(ctx, t, opt, name, job); err != nil {
			failed = append(failed, name+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckFailed, Message: "dropping " + strings.Join(failed, "; ")})
		return
	}
	rep.CleanedUp = true
	msg := "nothing was created"
	if len(created) > 0 {
		msg = "dropped " + strings.Join(created, ", ")
	}
	rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckPassed, Message: msg})
}
