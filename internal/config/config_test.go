package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cistern/internal/crypt"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestParseYAMLSubset(t *testing.T) {
	src := `
# comment
a: 1
b: "x # not comment"   # comment
c: 'it''s'
url: http://h:9091/p#frag
list:
  - one
  - [two, "three"]
  - k: v
    k2: v2
same_indent:
- x
- y
flow: {a: 1, b: [c, d], "e f": g}
empty:
nested:
  deeper:
    key: value
`
	n, err := parseYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path ...string) *node {
		cur := n
		for _, p := range path {
			cur = cur.m[p]
			if cur == nil {
				t.Fatalf("missing %v", path)
			}
		}
		return cur
	}
	checks := map[string]string{
		get("a").value:                       "1",
		get("b").value:                       "x # not comment",
		get("c").value:                       "it's",
		get("url").value:                     "http://h:9091/p#frag",
		get("list").items[0].value:           "one",
		get("list").items[1].items[1].value:  "three",
		get("list").items[2].m["k2"].value:   "v2",
		get("same_indent").items[1].value:    "y",
		get("flow").m["b"].items[1].value:    "d",
		get("flow").m["e f"].value:           "g",
		get("nested", "deeper", "key").value: "value",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if !get("empty").isNull() {
		t.Error("empty should be null")
	}
}

func TestParseYAMLErrors(t *testing.T) {
	for name, src := range map[string]string{
		"tab":       "a:\n\tb: 1",
		"dup":       "a: 1\na: 2",
		"block str": "a: |\n  x",
		"anchor":    "a: &x 1",
		"bad flow":  "a: [1, 2",
		"indent":    "a: 1\n  b: 2",
		"multi doc": "a: 1\n---\nb: 2",
	} {
		if _, err := parseYAML(src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

const minimal = `
repositories:
  local: { type: local, path: /tmp/r }
encryption:
  allow_plaintext: true
jobs:
  - name: db
    driver: postgres
    params:
      host: ${PGHOST:-localhost}
      port: ${PGPORT}
      databases: [a, b]
    hooks:
      post_restore: psql -c "select count(*) from ${TABLE}"
`

func TestParseDefaultsAndEnv(t *testing.T) {
	cfg, err := Parse([]byte(minimal), env(map[string]string{"PGPORT": "5433"}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := cfg.Job("db")
	if err != nil {
		t.Fatal(err)
	}
	if j.Repository != "local" || j.Timeout != 6*time.Hour || *cfg.Compression.Level != 6 || cfg.Metrics.Listen != ":9464" {
		t.Errorf("defaults not applied: %+v", j)
	}
	if j.Params["host"] != "localhost" || j.Params["port"] != "5433" || j.Params["databases"] != "a,b" {
		t.Errorf("params: %v", j.Params)
	}
	if !strings.Contains(j.Hooks.PostRestore, "${TABLE}") {
		t.Errorf("hooks must not be expanded: %q", j.Hooks.PostRestore)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unset var":        minimal, // PGPORT không có
		"unknown key":      strings.Replace(minimal, "driver: postgres", "driver: postgres\n    parms: {}", 1),
		"bad job name":     strings.Replace(minimal, "name: db", "name: DB", 1),
		"unknown repo":     strings.Replace(minimal, "driver: postgres", "driver: postgres\n    repository: nope", 1),
		"bad level":        minimal + "compression: { level: 12 }\n",
		"bad duration":     minimal + "defaults: { timeout: forever }\n",
		"bad type":         strings.Replace(minimal, "type: local", "type: ftp", 1),
		"duplicate job":    minimal + "  - name: db\n    driver: redis\n",
		"zero retention":   minimal + "defaults: { retention: { daily: 0 } }\n",
		"neg retention":    strings.Replace(minimal, "driver: postgres", "driver: postgres\n    retention: { last: -1, daily: 3 }", 1),
		"telegram no chat": minimal + "notify: { webhooks: [ { url: http://x, format: telegram } ] }\n",
		"bad on":           minimal + "notify: { webhooks: [ { url: http://x, on: [always] } ] }\n",
		"bad lock mode":    strings.Replace(minimal, "local: { type: local, path: /tmp/r }", "local: { type: s3, endpoint: http://m:9000, bucket: b, object_lock: { mode: STRICT, days: 3 } }", 1),
		"lock days":        strings.Replace(minimal, "local: { type: local, path: /tmp/r }", "local: { type: s3, endpoint: http://m:9000, bucket: b, object_lock: { mode: COMPLIANCE } }", 1),
		"bad endpoint":     strings.Replace(minimal, "local: { type: local, path: /tmp/r }", "local: { type: s3, endpoint: minio:9000, bucket: b }", 1),
		"bad schedule":     strings.Replace(minimal, "driver: postgres", "driver: postgres\n    schedule: \"0 25 * * *\"", 1),
		"bad verify":       strings.Replace(minimal, "driver: postgres", "driver: postgres\n    verify_schedule: \"@fortnightly\"", 1),
		"local lock":       strings.Replace(minimal, "local: { type: local, path: /tmp/r }", "local: { type: local, path: /tmp/r, object_lock: { mode: COMPLIANCE, days: 1 } }", 1),
	}
	for name, src := range cases {
		e := map[string]string{"PGPORT": "1"}
		if name == "unset var" {
			e = nil
		}
		if _, err := Parse([]byte(src), env(e)); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
}

func TestLoadRecipientsAndExample(t *testing.T) {
	id, _ := crypt.GenerateIdentity()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cistern.yaml")
	os.WriteFile(filepath.Join(dir, "recipients.txt"), []byte("# team\n"+id.Recipient().String()+"\n"), 0o600)
	os.WriteFile(cfgPath, []byte(strings.Replace(minimal, "allow_plaintext: true", "recipients_file: recipients.txt", 1)), 0o600)
	t.Setenv("PGPORT", "5432")
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Recipients) != 1 || cfg.Recipients[0].String() != id.Recipient().String() {
		t.Fatalf("recipients: %v", cfg.Recipients)
	}

	// Không recipient và không allow_plaintext → lỗi.
	os.WriteFile(cfgPath, []byte(strings.Replace(minimal, "allow_plaintext: true", "recipients: []", 1)), 0o600)
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected error without recipients")
	}

	// File ví dụ trong repo phải nạp được.
	for k, v := range map[string]string{
		"CISTERN_RECIPIENT": id.Recipient().String(), "S3_ACCESS_KEY_ID": "a", "S3_SECRET_ACCESS_KEY": "b",
		"SLACK_WEBHOOK_URL": "https://hooks.slack.com/x", "PGHOST": "db", "PGPASSWORD": "pw", "MYSQL_PASSWORD": "pw",
	} {
		t.Setenv(k, v)
	}
	ex, err := Load("../../examples/cistern.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := ex.Job("orders-db"); j == nil || j.Params["port"] != "5432" || j.Retention == nil || j.Retention.Daily != 7 {
		t.Fatalf("example job: %+v", j)
	}
}
