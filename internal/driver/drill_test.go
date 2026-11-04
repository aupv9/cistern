package driver

import (
	"errors"
	"testing"

	"cistern/internal/admin"
)

func TestSQLCheck(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	cases := []struct {
		c      DrillSQL
		out    string
		err    error
		status string
		msg    string
	}{
		{DrillSQL{Min: f(1)}, "5\n", nil, admin.CheckPassed, ""},
		{DrillSQL{Min: f(1), Max: f(4)}, "5", nil, admin.CheckFailed, "above the maximum"},
		{DrillSQL{Min: f(10)}, "5", nil, admin.CheckFailed, "below the minimum"},
		{DrillSQL{Min: f(1)}, "abc", nil, admin.CheckFailed, "the value is not a number"},
		{DrillSQL{Equals: s("1.50")}, "1.5", nil, admin.CheckPassed, ""},
		{DrillSQL{Equals: s("ok")}, "ok", nil, admin.CheckPassed, ""},
		{DrillSQL{Equals: s("ok")}, "NULL", nil, admin.CheckFailed, "the value differs"},
		{DrillSQL{}, "anything", nil, admin.CheckPassed, ""},
		{DrillSQL{}, "", nil, admin.CheckFailed, "the query returned no row"},
		{DrillSQL{}, "1\n2", nil, admin.CheckFailed, "the query returned more than one row"},
		{DrillSQL{}, "1\t2", nil, admin.CheckFailed, "the query returned more than one column"},
		{DrillSQL{}, "", errors.New("boom"), admin.CheckFailed, "boom"},
	}
	for i, c := range cases {
		got := SQLCheck(c.c, c.out, c.err, "\t")
		if got.Status != c.status || got.Message != c.msg || got.Kind != admin.KindSQL {
			t.Errorf("case %d: %+v", i, got)
		}
	}
	if e := (DrillSQL{Min: f(1), Max: f(2.5)}).Expectation(); e != "between 1 and 2.5" {
		t.Fatal(e)
	}
	if s := TrimSQL("  SELECT 1 ;; \n"); s != "SELECT 1" {
		t.Fatalf("%q", s)
	}
}
