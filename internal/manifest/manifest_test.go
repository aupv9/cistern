package manifest

import (
	"testing"
	"time"
)

func TestSnapshotID(t *testing.T) {
	ts := time.Date(2026, 11, 2, 3, 4, 5, 0, time.FixedZone("ICT", 7*3600))
	id := NewSnapshotID(ts)
	if !ValidSnapshotID(id) {
		t.Fatalf("invalid id %q", id)
	}
	if got := id[:16]; got != "20261101T200405Z" {
		t.Fatalf("id prefix = %q, want UTC time", got)
	}
}

func TestValidateJobName(t *testing.T) {
	for _, ok := range []string{"orders-db", "a", "pg.main_1"} {
		if err := ValidateJobName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "locks", "Orders", "-x", "a/b", string(make([]byte, 64))} {
		if ValidateJobName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateArtifactName(t *testing.T) {
	if err := ValidateArtifactName("db/orders.dump"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "../x", "a/./b", "a//b", "a b"} {
		if ValidateArtifactName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKeys(t *testing.T) {
	if got := ArtifactKey("p", "j", "s", "db/orders.dump", ArtifactSuffix(true, true)); got != "p/j/s/artifacts/db/orders.dump.gz.age" {
		t.Error(got)
	}
	if got := LockKey("p", "j"); got != "p/locks/j.lock" {
		t.Error(got)
	}
}
