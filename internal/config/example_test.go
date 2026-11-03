package config

import (
	"strings"
	"testing"
)

func TestExampleLoadsForServerAndDaemon(t *testing.T) {
	t.Setenv("CISTERN_RECIPIENT", "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p")
	for k, v := range map[string]string{"S3_ACCESS_KEY_ID": "a", "S3_SECRET_ACCESS_KEY": "b", "CISTERN_SESSION_KEY": strings.Repeat("s", 32), "CISTERN_OIDC_CLIENT_SECRET": "x", "CISTERN_DAEMON_TOKEN": strings.Repeat("t", 32)} {
		t.Setenv(k, v)
	}
	cfg, err := LoadFor("../../examples/cistern.yaml", ProcessServer)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Server.ValidateFor(false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"SLACK_WEBHOOK_URL": "https://h", "PGHOST": "db", "PGPASSWORD": "pw", "MYSQL_PASSWORD": "pw", "STAGING_PGHOST": "s", "STAGING_PGPASSWORD": "p"} {
		t.Setenv(k, v)
	}
	if _, err := LoadFor("../../examples/cistern.yaml", ProcessDaemon); err != nil {
		t.Fatal(err)
	}
}
