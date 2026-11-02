package cron

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	ict, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	from := time.Date(2026, 11, 2, 14, 37, 20, 0, ict) // Thứ Hai
	cases := []struct {
		expr string
		want string
	}{
		{"*/15 * * * *", "2026-11-02 14:45"},
		{"0 2 * * *", "2026-11-03 02:00"},
		{"@daily", "2026-11-03 00:00"},
		{"@hourly", "2026-11-02 15:00"},
		{"@weekly", "2026-11-08 00:00"}, // Chủ nhật kế tiếp
		{"@monthly", "2026-12-01 00:00"},
		{"@yearly", "2027-01-01 00:00"},
		{"30 14 * * *", "2026-11-03 14:30"},          // 14:30 hôm nay đã qua
		{"0 9-17/4 * * mon-fri", "2026-11-02 17:00"}, // 9, 13, 17 giờ; thứ Hai
		{"0 0 * * 7", "2026-11-08 00:00"},            // 7 = Chủ nhật
		{"0 0 31 * *", "2026-12-31 00:00"},           // tháng 11 không có ngày 31
		{"0 0 29 feb *", "2028-02-29 00:00"},         // năm nhuận
		{"0 0 1 * fri", "2026-11-06 00:00"},          // ngày 1 HOẶC thứ Sáu
		{"0 0 13 * 5", "2026-11-06 00:00"},           // cùng quy tắc OR
		{"5,10 3 * jan,nov *", "2026-11-03 03:05"},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := s.Next(from).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s: next = %s, want %s", c.expr, got, c.want)
		}
	}
}

func TestEveryAndNever(t *testing.T) {
	s, err := Parse("@every 90m")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(from.Add(90 * time.Minute)) {
		t.Error(got)
	}
	never, _ := Parse("0 0 30 2 *")
	if !never.Next(from).IsZero() {
		t.Error("Feb 30 should never fire")
	}
}

func TestParseErrors(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *", "a * * * *", "@sometimes", "@every", "@every -1h", "@every soon"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("%q accepted", expr)
		}
	}
}
