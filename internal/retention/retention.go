// Package retention: thuật toán GFS trên finished_at theo múi giờ của scheduler;
// giữ thêm mọi snapshot nằm trong depends_on của bản được giữ.
//
// Mỗi quy tắc duyệt snapshot từ mới đến cũ và giữ bản mới nhất của mỗi khoảng thời gian
// (giờ, ngày, tuần ISO, tháng, năm) cho đến khi đủ số lượng. "last" giữ N bản mới nhất.
// Một snapshot được giữ nếu ít nhất một quy tắc giữ nó.
package retention

import (
	"fmt"
	"sort"
	"time"
)

type Policy struct {
	Last, Hourly, Daily, Weekly, Monthly, Yearly int
}

func (p Policy) Empty() bool {
	return p.Last+p.Hourly+p.Daily+p.Weekly+p.Monthly+p.Yearly == 0
}

type Snapshot struct {
	ID         string
	FinishedAt time.Time
	DependsOn  []string
}

type Decision struct {
	Snapshot
	Keep    bool
	Reasons []string // last, hourly, daily, weekly, monthly, yearly, depends_on
}

type rule struct {
	name   string
	count  int
	bucket func(time.Time) string
}

// Apply trả về quyết định cho từng snapshot, mới nhất trước.
func Apply(p Policy, snaps []Snapshot, loc *time.Location) []Decision {
	if loc == nil {
		loc = time.Local
	}
	ds := make([]Decision, len(snaps))
	for i, s := range snaps {
		ds[i] = Decision{Snapshot: s}
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if !ds[i].FinishedAt.Equal(ds[j].FinishedAt) {
			return ds[i].FinishedAt.After(ds[j].FinishedAt)
		}
		return ds[i].ID > ds[j].ID
	})

	rules := []rule{
		{"last", p.Last, func(time.Time) string { return "" }},
		{"hourly", p.Hourly, func(t time.Time) string { return t.Format("2006-01-02 15") }},
		{"daily", p.Daily, func(t time.Time) string { return t.Format("2006-01-02") }},
		{"weekly", p.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) }},
		{"monthly", p.Monthly, func(t time.Time) string { return t.Format("2006-01") }},
		{"yearly", p.Yearly, func(t time.Time) string { return t.Format("2006") }},
	}
	for _, r := range rules {
		left := r.count
		last := "\x00"
		for i := range ds {
			if left <= 0 {
				break
			}
			b := r.bucket(ds[i].FinishedAt.In(loc))
			if r.name == "last" || b != last {
				ds[i].Keep = true
				ds[i].Reasons = append(ds[i].Reasons, r.name)
				last = b
				left--
			}
		}
	}

	// Giữ chuỗi phụ thuộc (incremental) của mọi bản được giữ.
	idx := map[string]int{}
	for i, d := range ds {
		idx[d.ID] = i
	}
	queue := []int{}
	for i, d := range ds {
		if d.Keep {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		for _, dep := range ds[i].DependsOn {
			j, ok := idx[dep]
			if !ok {
				continue
			}
			if !ds[j].Keep {
				ds[j].Keep = true
				queue = append(queue, j)
			}
			if !contains(ds[j].Reasons, "depends_on") {
				ds[j].Reasons = append(ds[j].Reasons, "depends_on")
			}
		}
	}
	return ds
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
