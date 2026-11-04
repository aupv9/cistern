package config

import (
	"fmt"
	"regexp"

	"cistern/internal/cron"
)

type Drill struct {
	Schedule      string        `yaml:"schedule"`
	Target        string        `yaml:"target"`
	RestoreSuffix string        `yaml:"restore_suffix"`
	MaxAgeDays    int           `yaml:"max_age_days"`
	Keep          bool          `yaml:"keep"`
	Checks        []*DrillCheck `yaml:"checks"`
}

type DrillCheck struct {
	Name     string   `yaml:"name"`
	SQL      string   `yaml:"sql"`
	Database string   `yaml:"database"`
	Min      *float64 `yaml:"min"`
	Max      *float64 `yaml:"max"`
	Equals   *string  `yaml:"equals"`
}

const (
	DefaultDrillSuffix     = "_drill"
	DefaultDrillMaxAgeDays = 35
)

var DrillDrivers = map[string]bool{"postgres": true, "mysql": true, "s3": true}

func SupportsDrill(driver string) bool { return DrillDrivers[driver] }

func SupportsDrillSQL(driver string) bool { return driver != "s3" }

var checkNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`)

func (j *Job) normalizeDrill(p string) []error {
	d := j.Drill
	if d == nil {
		return nil
	}
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	p += ".drill"
	if !SupportsDrill(j.Driver) {
		add("%s: the %s driver does not support drills (postgres, mysql and s3 do)", p, j.Driver)
		return errs
	}
	if d.Schedule != "" {
		if _, err := cron.Parse(d.Schedule); err != nil {
			add("%s.schedule: %v", p, err)
		}
	}
	if d.Target == "" {
		add("%s.target: required (a restore target with production: false)", p)
	} else if t, ok := j.Target(d.Target); !ok {
		add("%s.target: unknown restore target %q", p, d.Target)
	} else if t.IsProduction() {
		add("%s.target: %q is a production target; drills need a target with production: false", p, d.Target)
	}
	if d.RestoreSuffix == "" {
		d.RestoreSuffix = DefaultDrillSuffix
	}
	if !suffixRe.MatchString(d.RestoreSuffix) {
		add("%s.restore_suffix: must start with _ and match ^_[a-z0-9_]{1,30}$, got %q", p, d.RestoreSuffix)
	}
	if d.MaxAgeDays == 0 {
		d.MaxAgeDays = DefaultDrillMaxAgeDays
	}
	if d.MaxAgeDays < 1 {
		add("%s.max_age_days: must be at least 1", p)
	}
	if len(d.Checks) > 0 && !SupportsDrillSQL(j.Driver) {
		add("%s.checks: SQL checks are not supported by the %s driver", p, j.Driver)
	}
	names := map[string]bool{}
	for i, c := range d.Checks {
		cp := fmt.Sprintf("%s.checks[%d]", p, i)
		if c == nil {
			add("%s: empty", cp)
			continue
		}
		if !checkNameRe.MatchString(c.Name) {
			add("%s.name: required, letters, digits, space . _ - (at most 63), got %q", cp, c.Name)
		} else if names[c.Name] {
			add("%s.name: duplicate check %q", cp, c.Name)
		}
		names[c.Name] = true
		if c.SQL == "" {
			add("%s.sql: required", cp)
		}
		if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
			add("%s: min must not be greater than max", cp)
		}
		if c.Equals != nil && (c.Min != nil || c.Max != nil) {
			add("%s: use either equals or min/max", cp)
		}
	}
	return errs
}
