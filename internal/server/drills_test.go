package server

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/driver"
	"cistern/internal/manifest"
)

var drillFail atomic.Bool

func (d *uiDriver) Drill(ctx context.Context, src driver.Source, spec driver.DrillSpec) (*admin.DrillReport, error) {
	rep := &admin.DrillReport{}
	var n int64
	for _, name := range src.List() {
		rc, err := src.Open(name)
		if err != nil {
			return rep, err
		}
		c, _ := io.Copy(io.Discard, rc)
		n += c
		if err := rc.Close(); err != nil {
			return rep, err
		}
	}
	rep.RestoredBytes = n
	rep.Add(admin.DrillCheck{Name: "restore app", Kind: admin.KindRestore, Status: admin.CheckPassed})
	rep.AddTables([]admin.DrillTable{{Database: "app", Name: "public.t", Rows: 1000}})
	for _, c := range spec.Checks {
		out := "1"
		if drillFail.Load() {
			out = "0"
		}
		rep.Add(driver.SQLCheck(c, out, nil, "\t"))
	}
	rep.Add(admin.DrillCheck{Name: "cleanup", Kind: admin.KindCleanup, Status: admin.CheckPassed})
	rep.CleanedUp = true
	restoredMu.Lock()
	restored["drill:"+d.p["db_marker"]+":"+spec.Suffix] = d.p
	restoredMu.Unlock()
	return rep, nil
}

func withDrillAndPrune(prune bool) func(string) string {
	return func(s string) string {
		s = strings.Replace(s, "      - { name: prod, description: \"the real one\", production: true, set: { db_marker: app-prod } }\n",
			"      - { name: prod, description: \"the real one\", production: true, set: { db_marker: app-prod } }\n"+
				"    drill: { target: staging, restore_suffix: _dr, checks: [ { name: rows, sql: \"SELECT count(*) FROM t\", min: 1 } ] }\n", 1)
		if prune {
			s = strings.Replace(s, "  roles: [ { group: ops, roles: [operator] } ]\n", "  roles: [ { group: ops, roles: [operator] } ]\n  prune: { enabled: true }\n", 1)
		}
		return s
	}
}

func TestDrillsAndPrune(t *testing.T) {
	drillFail.Store(false)
	e := newEnv(t, withDaemon, nil, withDrillAndPrune(true))
	op := e.login("olivia@example.com", "operator")
	rest := e.login("rita@example.com", "restorer")
	adm := e.login("ada@example.com", "admin")
	if f := op.me.Deployment.Features; !f.Prune || !f.Drills {
		t.Fatalf("features: %+v", f)
	}

	var ref struct {
		RunID string `json:"run_id"`
	}
	op.json("POST", "/api/v1/jobs/app/backup", nil, 202, &ref)
	backup := op.wait(ref.RunID)

	op.expectError("POST", "/api/v1/jobs/app/drill", nil, 403, codeForbidden)
	rest.expectError("POST", "/api/v1/jobs/idle/drill", nil, 422, codeInvalid)
	rest.expectError("POST", "/api/v1/jobs/app/drill", map[string]string{"snapshot": "nope"}, 422, codeInvalid)

	rest.json("POST", "/api/v1/jobs/app/drill", map[string]string{}, 202, &ref)
	run := rest.wait(ref.RunID)
	if run.State != admin.StateSucceeded || run.Op != admin.OpDrill || run.Target != "staging" || run.Snapshot != backup.Snapshot || run.Drill == nil {
		t.Fatalf("drill run: %+v", run)
	}
	if d := run.Drill; d.State != admin.StateSucceeded || len(d.Checks) != 3 || d.Checks[1].Name != "rows" || d.Checks[1].Status != admin.CheckPassed ||
		!d.CleanedUp || d.RestoredBytes == 0 || len(d.Tables) != 1 || d.RequestedBy.Email != "rita@example.com" || d.Trigger != admin.TriggerUI {
		t.Fatalf("drill result: %+v", d)
	}
	restoredMu.Lock()
	p := restored["drill:app-staging:_dr"]
	restoredMu.Unlock()
	if p["host"] != "staging-db" || p["password"] != secrets["STAGING_PASSWORD"] {
		t.Fatalf("the daemon must drill into its own drill target, got %v", p["host"])
	}

	drillFail.Store(true)
	time.Sleep(1100 * time.Millisecond)
	rest.json("POST", "/api/v1/jobs/app/drill", map[string]string{"snapshot": "latest"}, 202, &ref)
	failed := rest.wait(ref.RunID)
	if failed.State != admin.StateFailed || !strings.Contains(failed.Error, `check "rows" failed`) || failed.Drill == nil || failed.Drill.Checks[1].Status != admin.CheckFailed {
		t.Fatalf("failed drill: %+v", failed)
	}

	var drills struct {
		Drills []admin.DrillResult `json:"drills"`
	}
	e.srv.data.invalidate("app")
	op.json("GET", "/api/v1/jobs/app/drills", nil, 200, &drills)
	if len(drills.Drills) != 2 || drills.Drills[0].RunID != failed.ID || drills.Drills[0].State != admin.StateFailed || drills.Drills[1].State != admin.StateSucceeded {
		t.Fatalf("drills: %+v", drills.Drills)
	}
	op.json("GET", "/api/v1/jobs/app/drills?limit=1", nil, 200, &drills)
	if len(drills.Drills) != 1 {
		t.Fatalf("limit: %d", len(drills.Drills))
	}
	var runs struct {
		Runs []admin.Run `json:"runs"`
	}
	op.json("GET", "/api/v1/jobs/app/runs?op=drill", nil, 200, &runs)
	if len(runs.Runs) != 2 || runs.Runs[0].Drill == nil {
		t.Fatalf("drill runs: %+v", runs.Runs)
	}

	var ov overviewResponse
	op.json("GET", "/api/v1/overview", nil, 200, &ov)
	var app jobSummary
	for _, j := range ov.Jobs {
		if j.Name == "app" {
			app = j
		}
	}
	if app.Health != HealthDrillFailing || app.LastDrill == nil || app.LastDrill.State != admin.StateFailed {
		t.Fatalf("app summary: %+v", app)
	}
	found := false
	for _, a := range ov.Alerts {
		found = found || (a.Kind == "drill_failing" && a.Job == "app")
	}
	if !found {
		t.Fatalf("alerts: %+v", ov.Alerts)
	}
	var det jobDetail
	op.json("GET", "/api/v1/jobs/app", nil, 200, &det)
	if det.Drill == nil || det.Drill.Target != "staging" || det.Drill.RestoreSuffix != "_dr" || det.Drill.MaxAgeDays != 35 || det.Drill.Checks != 1 ||
		det.LastDrillResult == nil || det.LastDrillResult.RunID != failed.ID {
		t.Fatalf("job detail: %+v %+v", det.Drill, det.LastDrillResult)
	}

	op.json("POST", "/api/v1/jobs/app/backup", nil, 202, &ref)
	op.wait(ref.RunID)
	time.Sleep(1100 * time.Millisecond)
	op.json("POST", "/api/v1/jobs/app/backup", nil, 202, &ref)
	op.wait(ref.RunID)
	e.srv.data.invalidate("app")
	e.srv.cfg.Jobs[0].Retention = &config.Retention{Last: 1}
	var prev prunePreview
	op.json("POST", "/api/v1/jobs/app/prune/preview", nil, 200, &prev)
	if prev.Job != "app" || prev.Kept != 1 || prev.Removed != 2 || prev.FreedBytes == 0 || prev.Locked != 0 || len(prev.Items) != 3 {
		t.Fatalf("prune preview: %+v", prev)
	}
	for _, it := range prev.Items {
		if it.StoredBytes == 0 || it.FinishedAt == nil || it.Reasons == nil {
			t.Fatalf("preview item: %+v", it)
		}
	}
	e.srv.cfg.Jobs[0].Retention = &config.Retention{Last: 5}

	rest.expectError("POST", "/api/v1/jobs/app/prune", map[string]string{"reason": "cleanup", "confirm_job": "app"}, 403, codeForbidden)
	er := adm.expectError("POST", "/api/v1/jobs/app/prune", map[string]string{"reason": "x", "confirm_job": "ap"}, 422, codeInvalid)
	if er.Error.Fields["reason"] == "" || er.Error.Fields["confirm_job"] == "" {
		t.Fatalf("fields: %+v", er)
	}
	adm.json("POST", "/api/v1/jobs/app/prune", map[string]string{"reason": "free space", "confirm_job": "app"}, 202, &ref)
	pr := adm.wait(ref.RunID)
	if pr.State != admin.StateSucceeded || pr.Op != admin.OpPrune || pr.Prune == nil || pr.Prune.Kept != 3 || pr.Prune.Removed != 0 {
		t.Fatalf("prune run: %+v %+v", pr, pr.Prune)
	}
	if es := e.auditEntries(ActionPrune); len(es) != 1 || es[0].Reason != "free space" || es[0].RunID != ref.RunID {
		t.Fatalf("prune.run audit: %+v", es)
	}
	if n := len(e.auditEntries(ActionDrill)); n != 2 {
		t.Fatalf("drill.run audit: %d", n)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, a := range e.auditEntries(ActionRunFinish) {
			if a.Params["op"] == admin.OpDrill || a.Params["op"] == admin.OpPrune {
				n++
			}
		}
		if n == 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run.finish entries for drills and prune: %+v", e.auditEntries(ActionRunFinish))
}

func TestPruneDisabledByDefault(t *testing.T) {
	e := newEnv(t, withDaemon, nil, withDrillAndPrune(false))
	adm := e.login("ada@example.com", "admin")
	if f := adm.me.Deployment.Features; f.Prune || !f.Drills {
		t.Fatalf("features: %+v", f)
	}
	er := adm.expectError("POST", "/api/v1/jobs/app/prune", map[string]string{"reason": "free space", "confirm_job": "app"}, 403, codeForbidden)
	if !strings.Contains(er.Error.Message, "server.prune.enabled") || !strings.Contains(er.Error.Message, "delete") {
		t.Fatalf("message: %q", er.Error.Message)
	}
	op := e.login("olivia@example.com", "operator")
	var prev prunePreview
	op.json("POST", "/api/v1/jobs/app/prune/preview", nil, 200, &prev)
	if prev.Items == nil || prev.Kept != 0 {
		t.Fatalf("empty preview: %+v", prev)
	}

	plain := newEnv(t, withDaemon, nil)
	u := plain.login("ada@example.com", "admin")
	if u.me.Deployment.Features.Drills {
		t.Fatal("drills feature without a drill section")
	}
	u.expectError("POST", "/api/v1/jobs/app/drill", nil, 422, codeInvalid)
}

func TestDrillHealth(t *testing.T) {
	e := newEnv(t, noDaemon, nil, withDrillAndPrune(false))
	j, _ := e.srv.cfg.Job("app")
	j.Drill.MaxAgeDays = 7
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	snap := &manifest.Manifest{ID: "20261025T020000Z-aaaa", StartedAt: now.Add(-10 * 24 * time.Hour), FinishedAt: now.Add(-10*24*time.Hour + time.Minute), Driver: "uitest"}
	fresh := &manifest.Manifest{ID: "20261104T020000Z-bbbb", StartedAt: now.Add(-10 * time.Hour), FinishedAt: now.Add(-10*time.Hour + time.Minute), Driver: "uitest"}
	d := &jobData{snaps: []*manifest.Manifest{snap, fresh}}
	kinds := func(alerts []alertView) string {
		var k []string
		for _, a := range alerts {
			if strings.HasPrefix(a.Kind, "drill_") {
				k = append(k, a.Kind)
			}
		}
		return strings.Join(k, ",")
	}
	sum, alerts := e.srv.summarize(j, d, daemonView{}, now)
	if sum.Health != HealthDrillFailing || kinds(alerts) != "drill_stale" || sum.DrillSchedule != "" || sum.LastDrill != nil {
		t.Fatalf("never drilled: %s %s", sum.Health, kinds(alerts))
	}
	drill := func(status string, ago time.Duration) *admin.Record {
		at := now.Add(-ago)
		return &admin.Record{ID: admin.NewID(at), Job: "app", Op: admin.OpDrill, Status: status, StartedAt: at, FinishedAt: at, Error: "check failed"}
	}
	d.runs = []*admin.Record{drill(admin.StateSucceeded, 48*time.Hour)}
	sum, alerts = e.srv.summarize(j, d, daemonView{}, now)
	if sum.Health != HealthVerifyOverdue || kinds(alerts) != "" || sum.LastDrill.State != admin.StateSucceeded {
		t.Fatalf("passed drill: %s %s", sum.Health, kinds(alerts))
	}
	d.runs = append(d.runs, drill(admin.StateFailed, time.Hour))
	sum, alerts = e.srv.summarize(j, d, daemonView{}, now)
	if sum.Health != HealthDrillFailing || kinds(alerts) != "drill_failing" || sum.LastDrill.State != admin.StateFailed {
		t.Fatalf("failed drill: %s %s", sum.Health, kinds(alerts))
	}
	sum, _ = e.srv.summarize(j, d, daemonView{}, now.Add(40*time.Hour))
	if sum.Health != HealthStale {
		t.Fatalf("stale backup wins over a failing drill: %s", sum.Health)
	}
	j.Drill = nil
	sum, alerts = e.srv.summarize(j, d, daemonView{}, now)
	if sum.Health != HealthVerifyOverdue || kinds(alerts) != "" || sum.LastDrill == nil {
		t.Fatalf("without a drill section: %s %s", sum.Health, kinds(alerts))
	}
}
