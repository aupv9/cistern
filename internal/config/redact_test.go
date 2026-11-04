package config

import (
	"strings"
	"testing"
)

func TestRedactYAML(t *testing.T) {
	src := `repositories:
  s3:
    type: s3
    endpoint: https://user:hunter2@s3.example.com
    access_key_id: AKIAPLAIN
    secret_access_key: literal-SECRET-1   # comment stays
    session_token: ${TOKEN}
notify:
  webhooks:
    - { url: "https://hooks.slack.com/services/T0/B0/literal-SECRET-2", format: slack }
    - { url: "https://api.telegram.org/bot${TG_TOKEN}/sendMessage", format: telegram, chat_id: "-100123" }
    - url: ${HOOK_URL}
server:
  session_key: ${SESSION_KEY:-literal-SECRET-3}
  oidc: { issuer: "https://idp.example.com", client_id: cistern, client_secret: 'literal-SECRET-4' }
  daemon: { url: "http://127.0.0.1:9465", token: "${DAEMON_TOKEN:-}" }
jobs:
  - name: orders-db
    driver: postgres
    params: { host: db, password: literal-SECRET-5, user: backup, ssl_key_file: /k.pem }
    hooks:
      pre_backup: echo hi
    restore_targets:
      - name: staging
        production: false
        set:
          password: ${STAGING_PASSWORD}
          api_key: literal-SECRET-6
`
	out, lits, err := RedactYAML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "literal-SECRET") || strings.Contains(out, "hunter2") {
		t.Fatalf("secret left in output:\n%s", out)
	}
	for _, keep := range []string{"${TOKEN}", "${TG_TOKEN}", "${HOOK_URL}", "${STAGING_PASSWORD}", "${DAEMON_TOKEN:-}", "AKIAPLAIN",
		"# comment stays", `chat_id: "-100123"`, "client_id: cistern", "ssl_key_file: /k.pem", "https://idp.example.com", "pre_backup: echo hi",
		`"${SESSION_KEY:-<redacted>}"`, "user: backup", `"https://hooks.slack.com/<redacted>"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("missing %q in:\n%s", keep, out)
		}
	}
	if _, err := parseYAML(out); err != nil {
		t.Fatalf("redacted output is not valid YAML: %v\n%s", err, out)
	}
	want := map[string]int{
		"repositories.s3.endpoint":                             4,
		"repositories.s3.secret_access_key":                    6,
		"notify.webhooks[0].url":                               10,
		"server.session_key":                                   14,
		"server.oidc.client_secret":                            15,
		"jobs[orders-db].params.password":                      20,
		"jobs[orders-db].restore_targets[staging].set.api_key": 28,
	}
	got := map[string]int{}
	for _, l := range lits {
		got[l.Path] = l.Line
	}
	for p, line := range want {
		if got[p] != line {
			t.Errorf("literal secret %s: line %d, want %d (all: %v)", p, got[p], line, lits)
		}
	}
	if len(got) != len(want) {
		t.Errorf("literal secrets: %v", lits)
	}
}
