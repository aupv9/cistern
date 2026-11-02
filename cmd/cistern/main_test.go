package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, exitUsage},
		{[]string{"version"}, exitOK},
		{[]string{"drivers"}, exitOK},
		{[]string{"bogus"}, exitUsage},
		{[]string{"backup"}, exitUsage},
		{[]string{"backup", "--job", "x", "--all"}, exitUsage},
		{[]string{"restore", "--job", "x", "--set", "novalue"}, exitUsage},
		{[]string{"list", "--job", "x", "-c", "/nonexistent.yaml"}, exitFail},
	}
	for _, c := range cases {
		if got, _, stderr := runCLI(c.args...); got != c.code {
			t.Errorf("%v: exit %d, want %d (stderr: %s)", c.args, got, c.code, stderr)
		}
	}
}

func TestDriversListed(t *testing.T) {
	_, out, _ := runCLI("drivers")
	for _, d := range []string{"elasticsearch", "etcd", "exec", "kafka", "mongodb", "mysql", "nats", "postgres", "rabbitmq", "redis", "s3"} {
		if !strings.Contains(out, d+"\n") {
			t.Errorf("driver %s missing", d)
		}
	}
}

func TestKeygenAndRestoreConfirmation(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id.txt")
	code, pub, _ := runCLI("keygen", "--out", key)
	if code != exitOK || !strings.HasPrefix(pub, "age1") {
		t.Fatalf("keygen: %d %q", code, pub)
	}
	if st, err := os.Stat(key); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st, err)
	}
	if code, _, _ := runCLI("keygen", "--out", key); code != exitFail {
		t.Fatal("keygen overwrote existing key")
	}

	cfg := filepath.Join(dir, "cistern.yaml")
	os.WriteFile(cfg, []byte("repositories:\n  l: {type: local, path: repo}\nencryption:\n  recipients: ["+strings.TrimSpace(pub)+"]\njobs:\n  - {name: db, driver: postgres}\n"), 0o600)
	code, _, stderr := runCLI("restore", "-c", cfg, "--job", "db")
	if code != exitFail || !strings.Contains(stderr, "--yes") {
		t.Fatalf("restore without --yes: %d %s", code, stderr)
	}
	code, out, _ := runCLI("list", "-c", cfg, "--job", "db", "--json")
	if code != exitOK || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list empty: %d %q", code, out)
	}
}
