package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHardeningDefaults(t *testing.T) {
	full := map[string]string{"PGPORT": "1", "DAEMON_TOKEN": strings.Repeat("t", 32), "SESSION_KEY": strings.Repeat("s", 32), "OIDC_SECRET": "x"}
	src := strings.Replace(serverConfig, "    driver: postgres\n", "    driver: postgres\n    run_history_days: 0\n", 1)
	src = strings.Replace(src, "jobs:\n", "jobs:\n  - name: other\n    driver: redis\n", 1)
	cfg, err := ParseFor([]byte(src), env(full), ProcessDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.APIListen != DefaultDaemonAPIListen {
		t.Fatalf("api_listen default: %q", cfg.Daemon.APIListen)
	}
	r := cfg.Server.RateLimit
	if r.Login() != 10 || r.Write() != 30 || r.Read() != 600 || len(cfg.Server.TrustedPrefixes) != 0 {
		t.Fatalf("rate limit defaults: %d %d %d", r.Login(), r.Write(), r.Read())
	}
	db, _ := cfg.Job("db")
	other, _ := cfg.Job("other")
	if *cfg.Defaults.RunHistoryDays != 400 || *db.RunHistoryDays != 0 || *other.RunHistoryDays != 400 {
		t.Fatalf("run_history_days: %d %d %d", *cfg.Defaults.RunHistoryDays, *db.RunHistoryDays, *other.RunHistoryDays)
	}

	noToken, err := Parse([]byte(minimal), env(map[string]string{"PGPORT": "1"}))
	if err != nil || noToken.Daemon.APIListen != "" {
		t.Fatalf("api_listen without a token: %q %v", noToken.Daemon.APIListen, err)
	}

	tuned := strings.Replace(serverConfig, "  name: production\n", `  name: production
  rate_limit: { login_per_minute: 0, write_per_minute: 5 }
  trusted_proxies: ["10.0.0.0/8", "192.168.1.7", "fd00::/8"]
`, 1) + "defaults: { run_history_days: 90 }\n"
	cfg, err = ParseFor([]byte(tuned), env(full), ProcessServer)
	if err != nil {
		t.Fatal(err)
	}
	r = cfg.Server.RateLimit
	if r.Login() != 0 || r.Write() != 5 || r.Read() != 600 {
		t.Fatalf("rate limits: %d %d %d", r.Login(), r.Write(), r.Read())
	}
	if p := cfg.Server.TrustedPrefixes; len(p) != 3 || p[1].String() != "192.168.1.7/32" || p[0].String() != "10.0.0.0/8" {
		t.Fatalf("trusted proxies: %v", p)
	}
	if db, _ := cfg.Job("db"); *db.RunHistoryDays != 90 {
		t.Fatalf("job run_history_days from defaults: %d", *db.RunHistoryDays)
	}
}

func TestHardeningRejects(t *testing.T) {
	full := map[string]string{"PGPORT": "1", "DAEMON_TOKEN": strings.Repeat("t", 32), "SESSION_KEY": strings.Repeat("s", 32), "OIDC_SECRET": "x"}
	srv := func(extra string) string {
		return strings.Replace(serverConfig, "  name: production\n", "  name: production\n  "+extra+"\n", 1)
	}
	dmn := func(extra string) string {
		return strings.Replace(serverConfig, "  api_token: ${DAEMON_TOKEN}\n", "  api_token: ${DAEMON_TOKEN}\n  "+extra+"\n", 1)
	}
	cases := map[string]string{
		"api_listen = metrics":   dmn(`api_listen: ":9464"`),
		"api_listen format":      dmn(`api_listen: "9465"`),
		"api_tls half":           dmn(`api_tls: { cert_file: c.pem }`),
		"negative rate":          srv(`rate_limit: { write_per_minute: -1 }`),
		"huge rate":              srv(`rate_limit: { read_per_minute: 1000000 }`),
		"bad proxy":              srv(`trusted_proxies: ["10.0.0.0/33"]`),
		"proxy hostname":         srv(`trusted_proxies: [ingress.local]`),
		"ca_file with http":      strings.Replace(serverConfig, `token: "${DAEMON_TOKEN}" }`, `token: "${DAEMON_TOKEN}", ca_file: ca.pem }`, 1),
		"negative run history":   serverConfig + "defaults: { run_history_days: -1 }\n",
		"negative job history":   strings.Replace(serverConfig, "    driver: postgres\n", "    driver: postgres\n    run_history_days: -5\n", 1),
		"short daemon api_token": strings.Replace(serverConfig, "  api_token: ${DAEMON_TOKEN}\n", "  api_token: short\n", 1),
	}
	for name, src := range cases {
		if _, err := ParseFor([]byte(src), env(full), ProcessServer); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
	ok := strings.Replace(serverConfig, `url: "http://cistern:9465", token: "${DAEMON_TOKEN}" }`, `url: "https://cistern:9465", token: "${DAEMON_TOKEN}", ca_file: ca.pem }`, 1)
	ok = strings.Replace(ok, "  api_token: ${DAEMON_TOKEN}\n", "  api_token: ${DAEMON_TOKEN}\n  api_listen: \"127.0.0.1:9999\"\n  api_tls: { cert_file: c.pem, key_file: k.pem }\n", 1)
	cfg, err := ParseFor([]byte(ok), env(full), ProcessServer)
	if err != nil || cfg.Server.Daemon.CAFile != "ca.pem" || cfg.Daemon.APITLS.KeyFile != "k.pem" || cfg.Daemon.APIListen != "127.0.0.1:9999" {
		t.Fatalf("tls settings: %v", err)
	}
}

func TestServerIgnoresEncryption(t *testing.T) {
	for k, v := range map[string]string{"PGPORT": "1", "DAEMON_TOKEN": strings.Repeat("t", 32), "SESSION_KEY": strings.Repeat("s", 32), "OIDC_SECRET": "x"} {
		t.Setenv(k, v)
	}
	t.Setenv("CISTERN_RECIPIENT", "")
	src := strings.Replace(serverConfig, "encryption:\n  allow_plaintext: true\n", "encryption:\n  recipients: [\"${CISTERN_RECIPIENT}\"]\n  recipients_file: missing.txt\n", 1)
	path := filepath.Join(t.TempDir(), "cistern.yaml")
	os.WriteFile(path, []byte(src), 0o600)
	cfg, err := LoadFor(path, ProcessServer)
	if err != nil {
		t.Fatalf("the server must not need encryption settings: %v", err)
	}
	if len(cfg.Recipients) != 0 {
		t.Fatal("the server parsed recipients")
	}
	if _, err := LoadFor(path, ProcessDaemon); err == nil {
		t.Fatal("the daemon accepted an unset recipient")
	}
}
