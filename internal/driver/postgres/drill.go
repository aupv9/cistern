package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cistern/internal/admin"
	"cistern/internal/driver"
)

const (
	drillStatementTimeout = "10min"
	fieldSep              = "\x1f"
)

const userTables = `c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')`

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

func (pg *Postgres) markerOf(ctx context.Context, name, marker string) (markerState, error) {
	out, err := pg.psql(ctx, pg.maintDB, "SELECT 'x' || coalesce(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = :'name';\n", map[string]string{"name": name})
	if err != nil {
		return markerAbsent, err
	}
	switch {
	case out == "":
		return markerAbsent, nil
	case out == "x"+marker:
		return markerOurs, nil
	}
	return markerForeign, nil
}

func (pg *Postgres) dropDrillDB(ctx context.Context, name, marker string) error {
	st, err := pg.markerOf(ctx, name, marker)
	if err != nil {
		return err
	}
	switch st {
	case markerAbsent:
		return nil
	case markerForeign:
		return fmt.Errorf("database %q lost its drill marker; not dropping it", name)
	}
	vars := map[string]string{"name": name}
	if _, err := pg.psql(ctx, pg.maintDB, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = :'name' AND pid <> pg_backend_pid();\n", vars); err != nil {
		return err
	}
	_, err = pg.psql(ctx, pg.maintDB, "DROP DATABASE IF EXISTS :\"name\";\n", vars)
	return err
}

func (pg *Postgres) Drill(ctx context.Context, src driver.Source, spec driver.DrillSpec) (*admin.DrillReport, error) {
	d := *pg
	d.clean = false
	return d.drill(ctx, src, spec)
}

func (pg *Postgres) drill(ctx context.Context, src driver.Source, spec driver.DrillSpec) (*admin.DrillReport, error) {
	rep := &admin.DrillReport{Checks: []admin.DrillCheck{}}
	dbs, _, err := pg.selectDatabases(src)
	if err != nil {
		return rep, err
	}
	if len(dbs) == 0 {
		return rep, errors.New("nothing to drill: the snapshot has no database artifacts")
	}
	if spec.Suffix == "" {
		return rep, errors.New("a drill needs a restore suffix")
	}
	marker := driver.DrillMarker(spec.Job)
	var targets []*drillDB
	for _, db := range dbs {
		t := &drillDB{source: db, target: db + spec.Suffix}
		if _, err := artifactName(t.target); err != nil {
			return rep, err
		}
		st, err := pg.markerOf(ctx, t.target, marker)
		if err != nil {
			return rep, err
		}
		if st == markerForeign {
			rep.CleanedUp = true
			rep.Add(admin.DrillCheck{Name: "restore " + db, Kind: admin.KindRestore, Status: admin.CheckFailed,
				Message: fmt.Sprintf("database %q already exists and has no drill marker of job %s; it was left untouched", t.target, spec.Job)})
			return rep, nil
		}
		t.stale = st == markerOurs
		targets = append(targets, t)
	}

	var restored atomic.Int64
	ok := true
	for _, t := range targets {
		if !ok {
			for _, k := range []string{admin.KindRestore, admin.KindTables, admin.KindRows} {
				rep.Add(admin.DrillCheck{Name: k + " " + t.source, Kind: k, Status: admin.CheckSkipped, Message: "an earlier database failed"})
			}
			continue
		}
		ok = pg.drillDatabase(ctx, src, t, marker, &restored, rep)
	}
	rep.RestoredBytes = restored.Load()

	for _, c := range spec.Checks {
		if !ok {
			rep.Add(admin.DrillCheck{Name: c.Name, Kind: admin.KindSQL, Status: admin.CheckSkipped, Expected: c.Expectation(), Message: "the restore failed"})
			continue
		}
		rep.Add(pg.sqlCheck(ctx, c, targets, spec.Suffix))
	}

	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	pg.cleanup(cctx, targets, marker, spec.Keep, rep)
	return rep, nil
}

func (pg *Postgres) drillDatabase(ctx context.Context, src driver.Source, t *drillDB, marker string, restored *atomic.Int64, rep *admin.DrillReport) bool {
	fail := func(kind, msg string) bool {
		rep.Add(admin.DrillCheck{Name: kind + " " + t.source, Kind: kind, Status: admin.CheckFailed, Message: msg})
		if kind == admin.KindRestore {
			for _, k := range []string{admin.KindTables, admin.KindRows} {
				rep.Add(admin.DrillCheck{Name: k + " " + t.source, Kind: k, Status: admin.CheckSkipped, Message: "the restore failed"})
			}
		}
		return false
	}
	vars := map[string]string{"name": t.target, "marker": marker}
	if t.stale {
		if err := pg.dropDrillDB(ctx, t.target, marker); err != nil {
			return fail(admin.KindRestore, "dropping the previous drill database "+t.target+": "+err.Error())
		}
	}
	if _, err := pg.psql(ctx, pg.maintDB, "CREATE DATABASE :\"name\";\n", vars); err != nil {
		return fail(admin.KindRestore, err.Error())
	}
	t.created = true
	if _, err := pg.psql(ctx, pg.maintDB, "COMMENT ON DATABASE :\"name\" IS :'marker';\n", vars); err != nil {
		return fail(admin.KindRestore, "marking the drill database: "+err.Error())
	}
	artifact := dbPrefix + t.source + dbSuffix
	before := restored.Load()
	if err := pg.pgRestoreCounted(ctx, src, artifact, t.target, restored); err != nil {
		return fail(admin.KindRestore, err.Error())
	}
	rep.Add(admin.DrillCheck{Name: admin.KindRestore + " " + t.source, Kind: admin.KindRestore, Status: admin.CheckPassed,
		Actual: strconv.FormatInt(restored.Load()-before, 10) + " bytes", Message: "pg_restore into " + t.target + " exited 0"})

	want, err := pg.tocTables(ctx, src, artifact)
	if err != nil {
		return fail(admin.KindTables, "reading the dump's table of contents: "+err.Error())
	}
	got, err := pg.psql(ctx, t.target, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE "+userTables+";\n", nil)
	if err != nil {
		return fail(admin.KindTables, err.Error())
	}
	chk := admin.DrillCheck{Name: admin.KindTables + " " + t.source, Kind: admin.KindTables, Expected: strconv.Itoa(want), Actual: got, Status: admin.CheckPassed}
	if got != strconv.Itoa(want) {
		chk.Status, chk.Message = admin.CheckFailed, "the restored database has a different number of tables than the dump"
	}
	rep.Add(chk)
	if chk.Status == admin.CheckFailed {
		return false
	}

	tables, err := pg.rowCounts(ctx, t)
	if err != nil {
		return fail(admin.KindRows, err.Error())
	}
	var total int64
	for _, tb := range tables {
		total += tb.Rows
	}
	rep.AddTables(tables)
	rep.Add(admin.DrillCheck{Name: admin.KindRows + " " + t.source, Kind: admin.KindRows, Status: admin.CheckPassed,
		Actual: fmt.Sprintf("%d rows in %d tables", total, len(tables))})
	return true
}

func (pg *Postgres) tocTables(ctx context.Context, src driver.Source, artifact string) (int, error) {
	rc, err := src.Open(artifact)
	if err != nil {
		return 0, err
	}
	out, err := driver.Output(ctx, pg.run, driver.Command{Name: "pg_restore", Args: []string{"-l"}, Env: pg.env(), Stdin: rc})
	driver.Abort(rc)
	if err != nil {
		return 0, err
	}
	return countTOCTables(out), nil
}

func countTOCTables(toc string) int {
	n := 0
	for _, l := range strings.Split(toc, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, ";") {
			continue
		}
		_, rest, ok := strings.Cut(l, ";")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 4 || f[2] != "TABLE" {
			continue
		}
		if f[3] == "DATA" || f[3] == "ATTACH" {
			continue
		}
		n++
	}
	return n
}

func (pg *Postgres) rowCounts(ctx context.Context, t *drillDB) ([]admin.DrillTable, error) {
	script := "SET statement_timeout = '" + drillStatementTimeout + "';\n" +
		"SELECT format('SELECT %L, count(*) FROM %I.%I', n.nspname || '.' || c.relname, n.nspname, c.relname) " +
		"FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE " + userTables + " ORDER BY 1 \\gexec\n"
	out, err := pg.psqlArgs(ctx, t.target, []string{"-F", fieldSep}, script, nil)
	if err != nil {
		return nil, err
	}
	var tables []admin.DrillTable
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		name, cnt, ok := strings.Cut(l, fieldSep)
		if !ok {
			continue
		}
		rows, err := strconv.ParseInt(strings.TrimSpace(cnt), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected row count %q for %s", cnt, name)
		}
		tables = append(tables, admin.DrillTable{Database: t.source, Name: name, Rows: rows})
	}
	return tables, nil
}

func (pg *Postgres) sqlCheck(ctx context.Context, c driver.DrillSQL, targets []*drillDB, suffix string) admin.DrillCheck {
	db := c.Database
	if db == "" {
		if len(targets) != 1 {
			names := make([]string, len(targets))
			for i, t := range targets {
				names[i] = t.source
			}
			return driver.SQLCheck(c, "", fmt.Errorf("set database: the snapshot has several databases (%s)", strings.Join(names, ", ")), fieldSep)
		}
		db = targets[0].source
	}
	found := false
	for _, t := range targets {
		found = found || t.source == db
	}
	if !found {
		return driver.SQLCheck(c, "", fmt.Errorf("database %q is not in the snapshot", db), fieldSep)
	}
	script := "SET statement_timeout = '" + drillStatementTimeout + "';\nBEGIN TRANSACTION READ ONLY;\n" + driver.TrimSQL(c.SQL) + ";\nCOMMIT;\n"
	out, err := pg.psqlArgs(ctx, db+suffix, []string{"-F", fieldSep, "-P", "null=NULL"}, script, nil)
	return driver.SQLCheck(c, out, err, fieldSep)
}

func (pg *Postgres) cleanup(ctx context.Context, targets []*drillDB, marker string, keep bool, rep *admin.DrillReport) {
	var created []string
	for _, t := range targets {
		if t.created {
			created = append(created, t.target)
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
		if err := pg.dropDrillDB(ctx, name, marker); err != nil {
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
