package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"cistern/internal/admin"
	"cistern/internal/metrics"
	"cistern/internal/s3/s3test"
)

const s3Drill = `    restore_targets:
      - { name: scratch, production: false, set: { restore_prefix: "scratch/", overwrite: "true" } }
      - { name: inside, production: false, set: { restore_prefix: "site/x/" } }
    drill: { target: scratch, max_age_days: 7 }
`

func TestS3DriverDrill(t *testing.T) {
	ctx := context.Background()
	srv := s3test.New(false)
	defer srv.Close()
	src := s3client(t, srv)
	put(t, src, "site/a.txt", "version 1", "text/plain")
	put(t, src, "site/dir/b.txt", "bee", "")
	e, c := s3DriverEngine(t, srv, true, s3Drill)
	backupS3(t, e, c)
	put(t, src, "site/c.txt", "sea", "")
	m := backupS3(t, e, c)
	drill := func() (*admin.DrillResult, error) {
		c.t = c.t.Add(time.Minute)
		return e.Drill(ctx, "site", "latest")
	}

	res, err := drill()
	if err != nil {
		t.Fatalf("drill: %v %+v", err, res)
	}
	var kinds []string
	for _, ch := range res.Checks {
		kinds = append(kinds, ch.Kind+"="+ch.Status)
	}
	if strings.Join(kinds, ",") != "restore=passed,objects=passed,cleanup=passed" || res.Snapshot != m.ID || res.Target != "scratch" ||
		res.State != admin.StateSucceeded || !res.CleanedUp || res.RestoredBytes != int64(len("version 1bee"+"sea")) {
		t.Fatalf("result: %+v", res)
	}
	if res.Checks[1].Expected != "3 objects, 15 bytes" || res.Checks[1].Actual != res.Checks[1].Expected {
		t.Fatalf("objects: %+v", res.Checks[1])
	}
	if keys, _ := src.ListObjects(ctx, "scratch/"); len(keys) != 0 {
		t.Fatalf("drill copy left behind: %v", keys)
	}
	if v, ok := e.Metrics.Get(metrics.DrillLastSuccess, metrics.Job("site")); !ok || v == 0 {
		t.Fatal("drill success metric not set")
	}

	put(t, src, "scratch/drill/unrelated.txt", "someone else's", "")
	res, err = drill()
	if err == nil || res.State != admin.StateFailed || !strings.Contains(res.Checks[0].Message, "left untouched") {
		t.Fatalf("foreign objects: %v %+v", err, res)
	}
	if keys, _ := src.ListObjects(ctx, "scratch/drill/"); len(keys) != 1 {
		t.Fatalf("foreign objects touched: %v", keys)
	}
	if _, ok := e.Metrics.Get(metrics.DrillLastFailure, metrics.Job("site")); !ok {
		t.Fatal("drill failure metric not set")
	}
	src.DeleteObject(ctx, "scratch/drill/unrelated.txt")

	e.Cfg.Jobs[0].Drill.Keep = true
	if res, err = drill(); err != nil || res.CleanedUp {
		t.Fatalf("keep: %v %+v", err, res)
	}
	if keys, _ := src.ListObjects(ctx, "scratch/drill/"); len(keys) != 4 {
		t.Fatalf("kept copy: %v", keys)
	}
	e.Cfg.Jobs[0].Drill.Keep = false
	if res, err = drill(); err != nil || !res.CleanedUp {
		t.Fatalf("drill over a kept copy: %v %+v", err, res)
	}

	e.Cfg.Jobs[0].Drill.Target = "inside"
	if res, err = drill(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping prefix: %v", err)
	}
	if keys, _ := src.ListObjects(ctx, "site/"); len(keys) != 3 {
		t.Fatalf("source changed: %v", keys)
	}

	recs, err := e.DrillHistory(ctx, "site")
	if err != nil || len(recs) != 5 {
		t.Fatalf("history: %d %v", len(recs), err)
	}
	if recs[0].Status != admin.StateFailed || recs[1].Status != admin.StateSucceeded || recs[1].Drill == nil || recs[1].Drill.Target != "scratch" {
		t.Fatalf("records: %+v %+v", recs[0], recs[1])
	}
	run := recs[1].Run()
	if run.Drill == nil || run.Drill.RunID != run.ID || len(run.Drill.Checks) != 3 {
		t.Fatalf("run view: %+v", run.Drill)
	}

	fresh, err := New(e.Cfg, "test", e.Log)
	if err != nil {
		t.Fatal(err)
	}
	fresh.RefreshMetrics(ctx)
	ok, _ := fresh.Metrics.Get(metrics.DrillLastSuccess, metrics.Job("site"))
	bad, _ := fresh.Metrics.Get(metrics.DrillLastFailure, metrics.Job("site"))
	if ok == 0 || bad == 0 {
		t.Fatalf("drill metrics not restored from run records: %v %v", ok, bad)
	}
}
