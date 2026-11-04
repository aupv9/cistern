package driver

import (
	"context"
	"io"
	"math"
	"strconv"
	"strings"
	"sync/atomic"

	"cistern/internal/admin"
)

type DrillSQL struct {
	Name     string
	SQL      string
	Database string
	Min      *float64
	Max      *float64
	Equals   *string
}

type DrillSpec struct {
	Job    string
	Suffix string
	Keep   bool
	Checks []DrillSQL
}

type Driller interface {
	Drill(ctx context.Context, src Source, spec DrillSpec) (*admin.DrillReport, error)
}

func DrillMarker(job string) string { return "cistern drill " + job }

func (c DrillSQL) Expectation() string {
	switch {
	case c.Equals != nil:
		return "= " + *c.Equals
	case c.Min != nil && c.Max != nil:
		return "between " + fmtFloat(*c.Min) + " and " + fmtFloat(*c.Max)
	case c.Min != nil:
		return ">= " + fmtFloat(*c.Min)
	case c.Max != nil:
		return "<= " + fmtFloat(*c.Max)
	}
	return "one value"
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func (c DrillSQL) Evaluate(value string) (bool, string) {
	num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	isNum := err == nil && !math.IsNaN(num)
	if c.Equals != nil {
		if value == *c.Equals {
			return true, ""
		}
		if want, err := strconv.ParseFloat(strings.TrimSpace(*c.Equals), 64); err == nil && isNum && want == num {
			return true, ""
		}
		return false, "the value differs"
	}
	if c.Min == nil && c.Max == nil {
		return true, ""
	}
	if !isNum {
		return false, "the value is not a number"
	}
	if c.Min != nil && num < *c.Min {
		return false, "below the minimum"
	}
	if c.Max != nil && num > *c.Max {
		return false, "above the maximum"
	}
	return true, ""
}

func TrimSQL(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return s
}

func SingleValue(out, colSep string) (string, string) {
	out = strings.TrimRight(out, "\r\n")
	if out == "" {
		return "", "the query returned no row"
	}
	if strings.Contains(out, "\n") {
		return "", "the query returned more than one row"
	}
	if colSep != "" && strings.Contains(out, colSep) {
		return "", "the query returned more than one column"
	}
	return out, ""
}

func SQLCheck(c DrillSQL, out string, runErr error, colSep string) admin.DrillCheck {
	chk := admin.DrillCheck{Name: c.Name, Kind: admin.KindSQL, Expected: c.Expectation()}
	if runErr != nil {
		chk.Status, chk.Message = admin.CheckFailed, runErr.Error()
		return chk
	}
	v, why := SingleValue(out, colSep)
	if why != "" {
		chk.Status, chk.Message = admin.CheckFailed, why
		return chk
	}
	if len(v) > 200 {
		chk.Actual = v[:200] + "…"
	} else {
		chk.Actual = v
	}
	if ok, why := c.Evaluate(v); !ok {
		chk.Status, chk.Message = admin.CheckFailed, why
		return chk
	}
	chk.Status = admin.CheckPassed
	return chk
}

type CountingReader struct {
	R io.Reader
	N *atomic.Int64
}

func (c CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	c.N.Add(int64(n))
	return n, err
}

type PrefixCounter struct {
	Prefix string
	Count  int
	pos    int
	dead   bool
}

func (p *PrefixCounter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c == '\n' {
			p.pos, p.dead = 0, false
			continue
		}
		if p.dead {
			continue
		}
		if p.pos < len(p.Prefix) && c == p.Prefix[p.pos] {
			p.pos++
			if p.pos == len(p.Prefix) {
				p.Count++
				p.dead = true
			}
			continue
		}
		p.dead = true
	}
	return len(b), nil
}
