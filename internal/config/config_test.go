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
		"unset var":        minimal,
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

	os.WriteFile(cfgPath, []byte(strings.Replace(minimal, "allow_plaintext: true", "recipients: []", 1)), 0o600)
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected error without recipients")
	}

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

const serverConfig = minimal + `
daemon:
  api_token: ${DAEMON_TOKEN}
server:
  name: production
  public_url: https://backup.example.com/
  tls: { upstream: true }
  session_key: ${SESSION_KEY}
  oidc: { issuer: "https://idp.example.com", client_id: cistern, client_secret: "${OIDC_SECRET}" }
  roles:
    - { group: dba, roles: [restorer, approver] }
  daemon: { url: "http://cistern:9465", token: "${DAEMON_TOKEN}" }
`

func TestServerSectionAndTargets(t *testing.T) {
	src := strings.Replace(serverConfig, "    hooks:", `    restore_targets:
      - { name: staging, production: false, set: { host: staging, password: "${STAGING_PW}" } }
      - { name: prod, set: {} }
    hooks:`, 1)
	full := map[string]string{"PGPORT": "1", "DAEMON_TOKEN": strings.Repeat("t", 32), "SESSION_KEY": strings.Repeat("s", 32), "OIDC_SECRET": "x", "STAGING_PW": "pw"}
	cfg, err := ParseFor([]byte(src), env(full), ProcessServer)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Server
	if s.Listen != DefaultServerListen || s.SessionTTL != 8*time.Hour || s.IdleTimeout != 30*time.Minute || s.StateRepository != "local" ||
		s.AuditRetentionDays != 400 || s.OIDC.GroupsClaim != "groups" || s.PublicURL != "https://backup.example.com" {
		t.Fatalf("server defaults: %+v", s)
	}
	if err := s.ValidateFor(false); err != nil {
		t.Fatal(err)
	}
	j, _ := cfg.Job("db")
	if st, _ := j.Target("staging"); st.IsProduction() || st.Set["password"] != "pw" {
		t.Fatalf("staging: %+v", st)
	}
	if p, _ := j.Target("prod"); !p.IsProduction() {
		t.Fatal("targets default to production")
	}
	ov, _, err := j.RestoreOverrides("staging", nil)
	if err != nil || ov["restore_suffix"] != DefaultRestoreSuffix || ov["host"] != "staging" {
		t.Fatalf("overrides: %v %v", ov, err)
	}
	empty := ""
	if ov, _, _ := j.RestoreOverrides("staging", &empty); ov["restore_suffix"] != "" {
		t.Fatalf("empty suffix: %v", ov)
	}
	bad := "x; drop"
	if _, _, err := j.RestoreOverrides("staging", &bad); err != ErrInvalidSuffix {
		t.Fatalf("bad suffix: %v", err)
	}
	if _, _, err := j.RestoreOverrides("nope", nil); err == nil {
		t.Fatal("unknown target accepted")
	}
	j.Driver = "s3"
	sfx := "_x"
	if _, _, err := j.RestoreOverrides("staging", &sfx); err != ErrSuffixUnsupported {
		t.Fatalf("s3 suffix: %v", err)
	}
	if ov, _, err := j.RestoreOverrides("staging", nil); err != nil || ov["restore_suffix"] != "" {
		t.Fatalf("s3 default: %v %v", ov, err)
	}

	only := map[string]string{"PGPORT": "1", "PGHOST": "h"}
	if _, err := ParseFor([]byte(src), env(only), ProcessCLI); err != nil {
		t.Fatalf("the CLI must not need server or daemon secrets: %v", err)
	}
	if _, err := ParseFor([]byte(src), env(only), ProcessDaemon); err == nil {
		t.Fatal("the daemon must need its api token")
	}
	srvOnly := map[string]string{"DAEMON_TOKEN": strings.Repeat("t", 32), "SESSION_KEY": strings.Repeat("s", 32), "OIDC_SECRET": "x"}
	if _, err := ParseFor([]byte(src), env(srvOnly), ProcessServer); err != nil {
		t.Fatalf("the server must not need database passwords: %v", err)
	}
	noSession := map[string]string{"PGPORT": "1", "DAEMON_TOKEN": strings.Repeat("t", 32), "OIDC_SECRET": "x", "STAGING_PW": "pw"}
	if _, err := ParseFor([]byte(src), env(noSession), ProcessServer); err == nil {
		t.Fatal("the server must need its session key")
	}

	for name, edit := range map[string][2]string{
		"short session key": {"session_key: ${SESSION_KEY}", "session_key: short"},
		"long ttl":          {"name: production", "name: production\n  session_ttl: 9h"},
		"bad role":          {"roles: [restorer, approver]", "roles: [root]"},
		"tls both":          {"tls: { upstream: true }", "tls: { upstream: true, cert_file: a, key_file: b }"},
		"short retention":   {"name: production", "name: production\n  audit_retention_days: 30"},
		"bad state repo":    {"name: production", "name: production\n  state_repository: nope"},
		"public url path":   {"https://backup.example.com/", "https://backup.example.com/ui"},
		"short token":       {"api_token: ${DAEMON_TOKEN}", "api_token: short"},
		"dup target":        {"{ name: prod, set: {} }", "{ name: staging, set: {} }"},
		"bad target name":   {"{ name: prod, set: {} }", "{ name: Prod, set: {} }"},
	} {
		if _, err := ParseFor([]byte(strings.Replace(src, edit[0], edit[1], 1)), env(full), ProcessServer); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
