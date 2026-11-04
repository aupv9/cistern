package postgres

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"cistern/internal/admin"
	"cistern/internal/driver"
)

type drillPG struct {
	dbs        map[string]string
	tables     map[string]string
	toc        string
	sqlOut     map[string]string
	failRestor bool
	dropped    []string
	restored   []string
}

func (f *drillPG) run(_ context.Context, c driver.Command) error {
	var stdin string
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		stdin = string(b)
	}
	vars := map[string]string{}
	var db string
	for i, a := range c.Args {
		if a == "-v" && i+1 < len(c.Args) {
			k, v, _ := strings.Cut(c.Args[i+1], "=")
			vars[k] = v
		}
		if a == "-d" && i+1 < len(c.Args) {
			db = c.Args[i+1]
		}
		if strings.HasPrefix(a, "--dbname=") {
			db = strings.TrimPrefix(a, "--dbname=")
		}
	}
	out := func(s string) error { _, err := io.WriteString(c.Stdout, s); return err }
	switch c.Name {
	case "pg_restore":
		if len(c.Args) == 1 && c.Args[0] == "-l" {
			return out(f.toc)
		}
		if f.failRestor {
			return errors.New("pg_restore: error: could not execute query")
		}
		f.restored = append(f.restored, db)
		return nil
	case "psql":
		switch {
		case strings.Contains(stdin, "shobj_description"):
			if c, ok := f.dbs[vars["name"]]; ok {
				return out("x" + c + "\n")
			}
			return nil
		case strings.Contains(stdin, "pg_terminate_backend"):
			return nil
		case strings.Contains(stdin, "DROP DATABASE"):
			delete(f.dbs, vars["name"])
			f.dropped = append(f.dropped, vars["name"])
			return nil
		case strings.Contains(stdin, "CREATE DATABASE"):
			f.dbs[vars["name"]] = ""
			return nil
		case strings.Contains(stdin, "COMMENT ON DATABASE"):
			f.dbs[vars["name"]] = vars["marker"]
			return nil
		case strings.Contains(stdin, "\\gexec"):
			return out(f.tables[db])
		case strings.Contains(stdin, "count(*) FROM pg_class"):
			return out(strconv.Itoa(strings.Count(f.tables[db], "\n")) + "\n")
		case strings.Contains(stdin, "READ ONLY"):
			for k, v := range f.sqlOut {
				if strings.Contains(stdin, k) {
					return out(v)
				}
			}
			return errors.New("ERROR: relation does not exist")
		}
	}
	return errors.New("unexpected command " + c.String())
}

const toc = `;
; Archive created at 2026-11-01
;
3; 2615 2200 SCHEMA - public pg_database_owner
216; 1259 16386 TABLE public orders postgres
217; 1259 16390 TABLE public items postgres
218; 1259 16395 VIEW public big_orders postgres
3350; 0 16386 TABLE DATA public orders postgres
3351; 0 16390 TABLE DATA public items postgres
`

func ptr[T any](v T) *T { return &v }

func newDrillPG() *drillPG {
	return &drillPG{
		dbs:    map[string]string{"postgres": "", "orders": ""},
		tables: map[string]string{"orders_drill": "public.items\x1f12\npublic.orders\x1f200000\n"},
		toc:    toc,
		sqlOut: map[string]string{"sum(amount)": "300000.50\n", "count(*) FROM orders": "200000\n"},
	}
}

func drillSpec(checks ...driver.DrillSQL) driver.DrillSpec {
	return driver.DrillSpec{Job: "orders-db", Suffix: "_drill", Checks: checks}
}

func statuses(rep *admin.DrillReport) string {
	var s []string
	for _, c := range rep.Checks {
		s = append(s, c.Name+"="+c.Status)
	}
	return strings.Join(s, ",")
}

func TestDrillPasses(t *testing.T) {
	f := newDrillPG()
	pg, _ := New(driver.Params{"databases": "orders", "clean": "true"}, f.run)
	src := memSource{files: map[string]string{"db/orders.dump": "DUMP", "globals.sql": "G"}}
	rep, err := pg.Drill(context.Background(), src, drillSpec(
		driver.DrillSQL{Name: "orders exist", SQL: "SELECT count(*) FROM orders;", Min: ptr(1.0)},
		driver.DrillSQL{Name: "total", SQL: "SELECT sum(amount) FROM orders", Equals: ptr("300000.5")},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(rep); got != "restore orders=passed,tables orders=passed,rows orders=passed,orders exist=passed,total=passed,cleanup=passed" {
		t.Fatalf("checks: %s\n%+v", got, rep.Checks)
	}
	if !rep.CleanedUp || rep.RestoredBytes != 4 || rep.TotalTables != 2 || rep.TotalRows != 200012 || len(rep.Tables) != 2 || rep.Tables[0].Name != "public.orders" || rep.Tables[0].Rows != 200000 {
		t.Fatalf("report: %+v", rep)
	}
	if _, left := f.dbs["orders_drill"]; left || len(f.restored) != 1 || f.restored[0] != "orders_drill" {
		t.Fatalf("dbs %v, restored %v", f.dbs, f.restored)
	}
	if rep.Checks[1].Expected != "2" || rep.Checks[1].Actual != "2" {
		t.Fatalf("tables check: %+v", rep.Checks[1])
	}
}

func TestDrillRefusesForeignDatabase(t *testing.T) {
	f := newDrillPG()
	f.dbs["orders_drill"] = ""
	pg, _ := New(driver.Params{}, f.run)
	rep, err := pg.Drill(context.Background(), memSource{files: map[string]string{"db/orders.dump": "DUMP"}}, drillSpec())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed() || !strings.Contains(rep.Checks[0].Message, "left untouched") || len(f.dropped) != 0 || len(f.restored) != 0 {
		t.Fatalf("report %+v, dropped %v", rep, f.dropped)
	}
	f.dbs["orders_drill"] = "cistern drill other-job"
	rep, _ = pg.Drill(context.Background(), memSource{files: map[string]string{"db/orders.dump": "DUMP"}}, drillSpec())
	if rep.Passed() || len(f.dropped) != 0 {
		t.Fatalf("another job's drill database must not be dropped: %+v", rep)
	}
}

func TestDrillReplacesOwnLeftoverAndKeeps(t *testing.T) {
	f := newDrillPG()
	f.dbs["orders_drill"] = "cistern drill orders-db"
	pg, _ := New(driver.Params{}, f.run)
	spec := drillSpec()
	spec.Keep = true
	rep, err := pg.Drill(context.Background(), memSource{files: map[string]string{"db/orders.dump": "DUMP"}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed() || rep.CleanedUp || len(f.dropped) != 1 || f.dbs["orders_drill"] != "cistern drill orders-db" {
		t.Fatalf("report %+v, dropped %v, dbs %v", rep, f.dropped, f.dbs)
	}
	if last := rep.Checks[len(rep.Checks)-1]; last.Kind != admin.KindCleanup || last.Status != admin.CheckSkipped {
		t.Fatalf("cleanup: %+v", last)
	}
}

func TestDrillFailures(t *testing.T) {
	f := newDrillPG()
	pg, _ := New(driver.Params{}, f.run)
	src := memSource{files: map[string]string{"db/orders.dump": "DUMP"}}
	rep, _ := pg.Drill(context.Background(), src, drillSpec(driver.DrillSQL{Name: "too many", SQL: "SELECT count(*) FROM orders", Max: ptr(10.0)}))
	if got := statuses(rep); got != "restore orders=passed,tables orders=passed,rows orders=passed,too many=failed,cleanup=passed" {
		t.Fatalf("checks: %s", got)
	}
	if c := rep.Checks[3]; c.Actual != "200000" || c.Expected != "<= 10" || c.Message != "above the maximum" {
		t.Fatalf("sql check: %+v", c)
	}
	if _, left := f.dbs["orders_drill"]; left {
		t.Fatal("drill database left behind after a failed check")
	}

	f.toc += "219; 1259 16399 TABLE public lost postgres\n"
	rep, _ = pg.Drill(context.Background(), src, drillSpec(driver.DrillSQL{Name: "x", SQL: "SELECT 1"}))
	if got := statuses(rep); got != "restore orders=passed,tables orders=failed,x=skipped,cleanup=passed" {
		t.Fatalf("checks: %s", got)
	}

	f = newDrillPG()
	f.failRestor = true
	pg, _ = New(driver.Params{}, f.run)
	rep, _ = pg.Drill(context.Background(), memSource{files: map[string]string{"db/orders.dump": "D", "db/users.dump": "U"}}, drillSpec())
	if got := statuses(rep); got != "restore orders=failed,tables orders=skipped,rows orders=skipped,restore users=skipped,tables users=skipped,rows users=skipped,cleanup=passed" {
		t.Fatalf("checks: %s", got)
	}
	if !rep.CleanedUp || len(f.dbs) != 2 {
		t.Fatalf("partial database left: %v", f.dbs)
	}
}

func TestDrillSQLNeedsDatabaseWithSeveral(t *testing.T) {
	f := newDrillPG()
	f.tables["users_drill"] = "public.u\x1f5\n"
	f.toc = "1; 1259 1 TABLE public t postgres\n"
	f.tables["orders_drill"] = "public.t\x1f1\n"
	f.tables["users_drill"] = "public.t\x1f5\n"
	pg, _ := New(driver.Params{}, f.run)
	src := memSource{files: map[string]string{"db/orders.dump": "D", "db/users.dump": "U"}}
	rep, _ := pg.Drill(context.Background(), src, drillSpec(
		driver.DrillSQL{Name: "no db", SQL: "SELECT count(*) FROM orders"},
		driver.DrillSQL{Name: "with db", SQL: "SELECT count(*) FROM orders", Database: "orders"},
	))
	byName := map[string]admin.DrillCheck{}
	for _, c := range rep.Checks {
		byName[c.Name] = c
	}
	if c := byName["no db"]; c.Status != admin.CheckFailed || !strings.Contains(c.Message, "set database") {
		t.Fatalf("no db: %+v", c)
	}
	if c := byName["with db"]; c.Status != admin.CheckPassed {
		t.Fatalf("with db: %+v", c)
	}
	if len(rep.Tables) != 2 || rep.Tables[0].Database != "users" {
		t.Fatalf("tables: %+v", rep.Tables)
	}
}

func TestCountTOCTables(t *testing.T) {
	extra := "300; 1259 1 TABLE ATTACH public part_2026 postgres\n301; 1259 2 TABLE public part_2026 postgres\n302; 1259 3 FOREIGN TABLE public f postgres\n"
	if n := countTOCTables(toc + extra); n != 3 {
		t.Fatalf("tables: %d", n)
	}
}
