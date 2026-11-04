package mysql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"cistern/internal/admin"
	"cistern/internal/driver"
)

type drillMy struct {
	schemas  map[string]string
	tables   map[string][]string
	rows     map[string]string
	lose     string
	sqlOut   string
	dropped  []string
	restored []string
}

var (
	quotedRe = regexp.MustCompile("'([^']*)'")
	tickRe   = regexp.MustCompile("`([^`]*)`")
	createRe = regexp.MustCompile("(?m)^CREATE TABLE `([^`]*)`")
)

func (f *drillMy) run(_ context.Context, c driver.Command) error {
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
	if c.Name != "mysql" {
		return fmt.Errorf("unexpected command %s", c.Name)
	}
	if sql == "" {
		b, _ := io.ReadAll(c.Stdin)
		stdin := string(b)
		switch {
		case strings.Contains(stdin, "START TRANSACTION READ ONLY"):
			return out(f.sqlOut)
		case db != "":
			f.restored = append(f.restored, db)
			for _, m := range createRe.FindAllStringSubmatch(stdin, -1) {
				if m[1] != f.lose {
					f.tables[db] = append(f.tables[db], m[1])
				}
			}
			return nil
		default:
			var lines []string
			for _, l := range strings.Split(strings.TrimSpace(stdin), "\n") {
				t := tickRe.FindAllStringSubmatch(l, -1)
				lines = append(lines, f.rows[t[1][1]])
			}
			return out(strings.Join(lines, "\n") + "\n")
		}
	}
	name := ""
	if m := quotedRe.FindStringSubmatch(sql); m != nil {
		name = m[1]
	}
	switch {
	case strings.Contains(sql, "information_schema.schemata"):
		if _, ok := f.schemas[name]; ok {
			return out("1\n")
		}
		return out("0\n")
	case strings.Contains(sql, "table_name = '_cistern_drill'"):
		if f.schemas[name] != "" {
			return out("1\n")
		}
		return out("0\n")
	case strings.HasPrefix(sql, "SELECT job FROM"):
		return out(f.schemas[tickRe.FindStringSubmatch(sql)[1]] + "\n")
	case strings.HasPrefix(sql, "DROP DATABASE"):
		n := tickRe.FindStringSubmatch(sql)[1]
		delete(f.schemas, n)
		delete(f.tables, n)
		f.dropped = append(f.dropped, n)
		return nil
	case strings.HasPrefix(sql, "CREATE DATABASE"):
		f.schemas[tickRe.FindStringSubmatch(sql)[1]] = ""
		return nil
	case strings.HasPrefix(sql, "CREATE TABLE"):
		n := tickRe.FindStringSubmatch(sql)[1]
		f.schemas[n] = quotedRe.FindStringSubmatch(sql[strings.Index(sql, "VALUES"):])[1]
		return nil
	case strings.HasPrefix(sql, "SELECT table_name"):
		return out(strings.Join(f.tables[name], "\n") + "\n")
	}
	return errors.New("unexpected query " + sql)
}

const dump = "-- Cistern: character set\nALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n" +
	"DROP TABLE IF EXISTS `orders`;\nCREATE TABLE `orders` (\n  `id` int NOT NULL\n);\nINSERT INTO `orders` VALUES (1),(2);\n" +
	"/*!50001 CREATE TABLE `big_orders` (\n `id` tinyint NOT NULL\n) */;\n" +
	"CREATE TABLE `audit` (\n  `id` int\n);\n"

func newDrillMy() *drillMy {
	return &drillMy{
		schemas: map[string]string{"orders": ""}, tables: map[string][]string{},
		rows: map[string]string{"orders": "100000", "audit": "0"}, sqlOut: "100000\n",
	}
}

func TestMySQLDrill(t *testing.T) {
	f := newDrillMy()
	m := newDriver(t, &fakeMySQL{}, driver.Params{"client": "mysql"})
	m.run = f.run
	src := memSource{files: map[string]string{"db/orders.sql": dump}}
	min := 1.0
	spec := driver.DrillSpec{Job: "shop", Suffix: "_drill", Checks: []driver.DrillSQL{{Name: "orders", SQL: "SELECT COUNT(*) FROM orders;", Min: &min}}}
	rep, err := m.Drill(context.Background(), src, spec)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range rep.Checks {
		got = append(got, c.Name+"="+c.Status)
	}
	if strings.Join(got, ",") != "restore orders=passed,tables orders=passed,rows orders=passed,orders=passed,cleanup=passed" {
		t.Fatalf("checks: %v %+v", got, rep.Checks)
	}
	if rep.Checks[1].Expected != "2" || rep.RestoredBytes != int64(len(dump)) || !rep.CleanedUp || len(rep.Tables) != 2 || rep.Tables[0].Rows != 100000 {
		t.Fatalf("report: %+v", rep)
	}
	if _, left := f.schemas["orders_drill"]; left {
		t.Fatal("drill database left behind")
	}

	f.lose = "audit"
	rep, _ = m.Drill(context.Background(), src, spec)
	if rep.Passed() || rep.Checks[1].Status != admin.CheckFailed || rep.Checks[1].Actual != "1" {
		t.Fatalf("lost table not detected: %+v", rep.Checks)
	}

	f.lose = ""
	f.schemas["orders_drill"] = ""
	f.dropped = nil
	rep, _ = m.Drill(context.Background(), src, spec)
	if rep.Passed() || !strings.Contains(rep.Checks[0].Message, "left untouched") || len(f.dropped) != 0 {
		t.Fatalf("foreign database: %+v dropped %v", rep.Checks, f.dropped)
	}
	f.schemas["orders_drill"] = "shop"
	rep, _ = m.Drill(context.Background(), src, spec)
	if !rep.Passed() || len(f.dropped) != 2 {
		t.Fatalf("own leftover: %+v dropped %v", rep.Checks, f.dropped)
	}
}

func TestPrefixCounterAcrossWrites(t *testing.T) {
	pc := &driver.PrefixCounter{Prefix: "CREATE TABLE "}
	for _, chunk := range strings.SplitAfter(dump, "E") {
		pc.Write([]byte(chunk))
	}
	if pc.Count != 2 {
		t.Fatalf("count %d", pc.Count)
	}
}
