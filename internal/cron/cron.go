// Package cron: biểu thức cron 5 trường (phút giờ ngày tháng thứ), các macro @yearly @monthly
// @weekly @daily @hourly và @every <duration>.
//
// Mỗi trường nhận *, số, khoảng a-b, bước */n hoặc a-b/n hoặc a/n, danh sách cách nhau bởi dấu phẩy;
// tháng và thứ nhận tên tiếng Anh (jan, mon). Thứ 0 và 7 đều là Chủ nhật. Như cron chuẩn: khi cả
// ngày-trong-tháng và thứ đều bị giới hạn, khớp một trong hai là đủ.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule interface {
	// Next trả về thời điểm chạy kế tiếp sau t (cùng múi giờ với t); zero nếu không bao giờ.
	Next(t time.Time) time.Time
}

type every time.Duration

func (e every) Next(t time.Time) time.Time { return t.Add(time.Duration(e)) }

type spec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if rest, ok := strings.CutPrefix(expr, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("cron %q: invalid duration", expr)
		}
		return every(d), nil
	}
	if m, ok := macros[expr]; ok {
		expr = m
	} else if strings.HasPrefix(expr, "@") {
		return nil, fmt.Errorf("cron %q: unknown macro", expr)
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields (minute hour day month weekday), got %d", expr, len(f))
	}
	var s spec
	var err error
	fields := []struct {
		name     string
		dst      *uint64
		min, max int
		names    map[string]int
	}{
		{"minute", &s.minute, 0, 59, nil},
		{"hour", &s.hour, 0, 23, nil},
		{"day of month", &s.dom, 1, 31, nil},
		{"month", &s.month, 1, 12, monthNames},
		{"day of week", &s.dow, 0, 7, dowNames},
	}
	for i, fd := range fields {
		if *fd.dst, err = parseField(f[i], fd.min, fd.max, fd.names); err != nil {
			return nil, fmt.Errorf("cron %q: %s: %w", expr, fd.name, err)
		}
	}
	if s.dow&(1<<7) != 0 { // 7 = Chủ nhật
		s.dow |= 1
	}
	s.domStar, s.dowStar = f[2] == "*" || strings.HasPrefix(f[2], "*/"), f[4] == "*" || strings.HasPrefix(f[4], "*/")
	return &s, nil
}

func parseValue(v string, names map[string]int) (int, error) {
	if n, ok := names[strings.ToLower(v)]; ok {
		return n, nil
	}
	return strconv.Atoi(v)
}

func parseField(field string, min, max int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			var err error
			if step, err = strconv.Atoi(stepStr); err != nil || step < 1 {
				return 0, fmt.Errorf("invalid step %q", stepStr)
			}
		}
		lo, hi := min, max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err1, err2 error
			lo, err1 = parseValue(a, names)
			hi, err2 = parseValue(b, names)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("invalid range %q", rng)
			}
		default:
			v, err := parseValue(rng, names)
			if err != nil {
				return 0, fmt.Errorf("invalid value %q", rng)
			}
			lo = v
			if !hasStep {
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("%q out of range %d-%d", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	if bits == 0 {
		return 0, errors.New("empty field")
	}
	return bits, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (s *spec) dayMatches(t time.Time) bool {
	dom, dow := has(s.dom, t.Day()), has(s.dow, int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	default:
		return dom || dow
	}
}

func (s *spec) Next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0) // vd. "0 0 30 2 *" không bao giờ khớp
	for t.Before(limit) {
		switch {
		case !has(s.month, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case !has(s.hour, t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		case !has(s.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
