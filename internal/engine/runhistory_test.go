package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cistern/internal/admin"
	"cistern/internal/repo"
	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func writeOldRecord(t *testing.T, r repo.Repository, prefix, job, op string, at time.Time) string {
	t.Helper()
	rec := &admin.Record{ID: admin.NewID(at), Job: job, Op: op, Status: admin.StateSucceeded, Trigger: admin.TriggerSchedule, StartedAt: at, FinishedAt: at}
	if err := admin.WriteRecord(context.Background(), r, prefix, rec); err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

func recordCount(t *testing.T, r repo.Repository, prefix, job string) int {
	t.Helper()
	keys, err := admin.RecordKeys(context.Background(), r, prefix, job)
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

func TestPruneRunHistoryKeepsNewest30PerOp(t *testing.T) {
	e, repoDir, c := newEngine(t, `    driver: fake
    run_history_days: 10
    params: { artifacts: "a" }
`, "")
	ctx := context.Background()
	r, prefix, err := e.repoFor(e.Cfg.Jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	for d := 1; d <= 50; d++ {
		writeOldRecord(t, r, prefix, "app", admin.OpBackup, c.t.Add(-time.Duration(d)*day))
	}
	for d := 100; d < 105; d++ {
		writeOldRecord(t, r, prefix, "app", admin.OpVerify, c.t.Add(-time.Duration(d)*day))
	}
	writeOldRecord(t, r, prefix, "app", admin.OpRestore, c.t.Add(-200*day))
	garbage := admin.NewID(c.t.Add(-300 * day))
	os.WriteFile(filepath.Join(repoDir, "_admin", "runs", "app", garbage+".json"), []byte("not json"), 0o644)
	if n := recordCount(t, r, prefix, "app"); n != 57 {
		t.Fatalf("setup: %d records", n)
	}

	rep, err := e.Prune(ctx, "app", true)
	if err != nil || rep.RunRecordsRemoved != 20 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if n := recordCount(t, r, prefix, "app"); n != 57 {
		t.Fatalf("dry run removed run records: %d left", n)
	}

	rep, err = e.Prune(ctx, "app", false)
	if err != nil || rep.RunRecordsRemoved != 20 || rep.Errors != 0 {
		t.Fatalf("prune: %+v %v", rep, err)
	}
	recs, _ := admin.ListRecords(ctx, r, prefix, "app")
	ops := map[string]int{}
	var oldestBackup time.Time
	for _, rec := range recs {
		ops[rec.Op]++
		if rec.Op == admin.OpBackup && (oldestBackup.IsZero() || rec.StartedAt.Before(oldestBackup)) {
			oldestBackup = rec.StartedAt
		}
	}
	if ops[admin.OpBackup] != 30 || ops[admin.OpVerify] != 5 || ops[admin.OpRestore] != 1 || ops[admin.OpPrune] != 1 {
		t.Fatalf("kept: %v", ops)
	}
	if want := c.t.Add(-30 * day); !oldestBackup.Equal(want) {
		t.Fatalf("oldest backup kept %v, want %v", oldestBackup, want)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "_admin", "runs", "app", garbage+".json")); err != nil {
		t.Fatal("an unreadable record was removed")
	}

	c.t = c.t.Add(400 * day)
	zero := 0
	e.Cfg.Jobs[0].RunHistoryDays = &zero
	if rep, err := e.Prune(ctx, "app", false); err != nil || rep.RunRecordsRemoved != 0 {
		t.Fatalf("run_history_days 0 must keep everything: %+v %v", rep, err)
	}
}

func TestRunRecordsObjectLockAndPrune(t *testing.T) {
	ctx := context.Background()
	srv := s3test.New(true)
	defer srv.Close()
	e, c := newS3Engine(t, srv, "    object_lock: { mode: GOVERNANCE, days: 3 }")
	if _, err := e.Backup(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	var recKey string
	for _, k := range srv.Keys() {
		if strings.HasPrefix(k, "cistern/_admin/runs/app/") {
			recKey = k
		}
	}
	v, ok := srv.Current(recKey)
	if !ok || v.LockMode != s3.LockGovernance || v.RetainUntil.Before(time.Now().Add(71*time.Hour)) || v.RetainUntil.After(time.Now().Add(73*time.Hour)) {
		t.Fatalf("run record %q not locked for 3 days: %+v", recKey, v)
	}

	r, prefix, _ := e.repoFor(e.Cfg.Jobs[0])
	one := 1
	e.Cfg.Jobs[0].RunHistoryDays = &one
	for d := 2; d < 37; d++ {
		writeOldRecord(t, r, prefix, "app", admin.OpBackup, c.t.Add(-time.Duration(d)*24*time.Hour))
	}
	srv.Inject(s3test.Fault{Method: "DELETE", Status: 403})
	rep, err := e.Prune(ctx, "app", false)
	if err != nil || rep.Errors != 0 || rep.RunRecordsRemoved != 5 {
		t.Fatalf("prune with a locked run record: %+v %v", rep, err)
	}
}
