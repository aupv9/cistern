package retention

import (
	"fmt"
	"testing"
	"time"
)

// Một snapshot mỗi 6 giờ trong 2 năm, mốc 2026-11-02 00:00 UTC.
func series(n int, step time.Duration) []Snapshot {
	end := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	out := make([]Snapshot, n)
	for i := range out {
		t := end.Add(-time.Duration(i) * step)
		out[i] = Snapshot{ID: t.Format("20060102T150405Z") + "-0000", FinishedAt: t}
	}
	return out
}

func kept(ds []Decision) map[string]Decision {
	m := map[string]Decision{}
	for _, d := range ds {
		if d.Keep {
			m[d.ID] = d
		}
	}
	return m
}

func TestGFS(t *testing.T) {
	snaps := series(4*730, 6*time.Hour)
	ds := Apply(Policy{Last: 3, Daily: 7, Weekly: 4, Monthly: 6, Yearly: 2}, snaps, time.UTC)
	k := kept(ds)

	count := map[string]int{}
	for _, d := range k {
		for _, r := range d.Reasons {
			count[r]++
		}
	}
	want := map[string]int{"last": 3, "daily": 7, "weekly": 4, "monthly": 6, "yearly": 2}
	for r, n := range want {
		if count[r] != n {
			t.Errorf("%s: kept %d, want %d", r, count[r], n)
		}
	}
	if !ds[0].Keep || ds[0].ID != snaps[0].ID {
		t.Error("newest snapshot must be kept")
	}
	// daily giữ bản mới nhất của mỗi ngày: 2026-11-02 00:00, 2026-11-01 18:00, ...
	if d := k["20261101T180000Z-0000"]; !contains(d.Reasons, "daily") {
		t.Errorf("expected newest of 2026-11-01 kept daily, got %v", d.Reasons)
	}
	if _, ok := k["20261031T120000Z-0000"]; ok { // 10-31 18:00 mới hơn cùng ngày; last=3 không với tới
		t.Error("older snapshot of the same day kept")
	}
	// Tổng số giữ ≤ tổng quy tắc (có thể trùng).
	if len(k) > 3+7+4+6+2 {
		t.Errorf("kept %d", len(k))
	}
}

func TestTimezoneBuckets(t *testing.T) {
	// 17:30 UTC và 16:30 UTC ngày 1/11 là 00:30 và 23:30 theo giờ Việt Nam: hai ngày khác nhau.
	a := Snapshot{ID: "a", FinishedAt: time.Date(2026, 11, 1, 17, 30, 0, 0, time.UTC)}
	b := Snapshot{ID: "b", FinishedAt: time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)}
	ict, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	if n := len(kept(Apply(Policy{Daily: 5}, []Snapshot{a, b}, ict))); n != 2 {
		t.Errorf("ICT: kept %d, want 2", n)
	}
	if n := len(kept(Apply(Policy{Daily: 5}, []Snapshot{a, b}, time.UTC))); n != 1 {
		t.Errorf("UTC: kept %d, want 1", n)
	}
}

func TestDependsOnChain(t *testing.T) {
	snaps := series(5, 24*time.Hour)
	// s0 → s2 → s4 (incremental)
	snaps[0].DependsOn = []string{snaps[2].ID}
	snaps[2].DependsOn = []string{snaps[4].ID, "missing"}
	k := kept(Apply(Policy{Last: 1}, snaps, time.UTC))
	for _, i := range []int{0, 2, 4} {
		if _, ok := k[snaps[i].ID]; !ok {
			t.Errorf("snapshot %d not kept", i)
		}
	}
	if len(k) != 3 {
		t.Errorf("kept %d: %v", len(k), fmt.Sprint(k))
	}
}

func TestEmptyPolicyKeepsNothing(t *testing.T) {
	if !(Policy{}).Empty() {
		t.Fatal("Empty")
	}
	if len(kept(Apply(Policy{}, series(3, time.Hour), time.UTC))) != 0 {
		t.Fatal("empty policy kept snapshots")
	}
}
