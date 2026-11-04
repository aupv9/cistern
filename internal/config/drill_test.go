package config

import (
	"strings"
	"testing"
)

const drillBase = `
repositories:
  local: { type: local, path: /r }
encryption: { allow_plaintext: true }
jobs:
  - name: db
    driver: postgres
    restore_targets:
      - { name: scratch, production: false }
      - { name: live, production: true }
`

func TestDrillConfig(t *testing.T) {
	cfg, err := Parse([]byte(drillBase+`    drill:
      schedule: "0 4 * * 0"
      target: scratch
      checks:
        - { name: orders exist, sql: "SELECT count(*) FROM orders", min: 1 }
        - { name: total, sql: "SELECT 1", database: orders, equals: "1" }
        - { name: ratio, sql: "SELECT 0.5", min: 0.25, max: 1e3 }
`), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Jobs[0].Drill
	if d.RestoreSuffix != "_drill" || d.MaxAgeDays != 35 || d.Keep || len(d.Checks) != 3 || *d.Checks[0].Min != 1 || *d.Checks[1].Equals != "1" ||
		*d.Checks[2].Min != 0.25 || *d.Checks[2].Max != 1000 || d.Checks[1].Database != "orders" {
		t.Fatalf("drill: %+v", d)
	}

	bad := map[string]string{
		"target: live":                                                            "production target",
		"target: nope":                                                            "unknown restore target",
		"target: scratch, restore_suffix: drill":                                  "must start with _",
		"target: scratch, restore_suffix: _Drill":                                 "must start with _",
		"target: scratch, max_age_days: -1":                                       "max_age_days",
		"target: scratch, schedule: \"bad\"":                                      "drill.schedule",
		"schedule: \"@daily\"":                                                    "drill.target: required",
		"target: scratch, checks: [ { name: x } ]":                                "sql: required",
		"target: scratch, checks: [ { sql: x } ]":                                 "name: required",
		"target: scratch, checks: [ { name: x, sql: y, min: 5, max: 1 } ]":        "min must not be greater",
		"target: scratch, checks: [ { name: x, sql: y, min: 1, equals: \"1\" } ]": "either equals or min/max",
		"target: scratch, checks: [ { name: x, sql: y }, { name: x, sql: z } ]":   "duplicate check",
		"target: scratch, checks: [ { name: x, sql: y, min: abc } ]":              "expected a number",
	}
	for drill, want := range bad {
		_, err := Parse([]byte(drillBase+"    drill: { "+drill+" }\n"), env(nil))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", drill, err, want)
		}
	}

	_, err = Parse([]byte(`
repositories:
  local: { type: local, path: /r }
encryption: { allow_plaintext: true }
jobs:
  - name: cache
    driver: redis
    restore_targets: [ { name: scratch, production: false } ]
    drill: { target: scratch }
  - name: site
    driver: s3
    params: { endpoint: "http://s3", bucket: b }
    restore_targets: [ { name: scratch, production: false, set: { restore_prefix: drills/ } } ]
    drill: { target: scratch, checks: [ { name: x, sql: y } ] }
`), env(nil))
	if err == nil || !strings.Contains(err.Error(), "redis driver does not support drills") || !strings.Contains(err.Error(), "SQL checks are not supported by the s3 driver") {
		t.Fatalf("unsupported: %v", err)
	}

	cfg, err = Parse([]byte(drillBase+"server:\n  prune: { enabled: true }\n"), env(nil))
	if err != nil || !cfg.Server.Prune.Enabled {
		t.Fatalf("server.prune: %v", err)
	}
}
