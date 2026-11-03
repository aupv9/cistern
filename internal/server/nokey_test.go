package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerNeverLoadsPrivateKeys(t *testing.T) {
	var files []string
	filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) < 5 {
		t.Fatalf("found only %v", files)
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, banned := range []string{"LoadIdentities", "engine.New(", "ParseIdentities", "identity_file", "IdentityFile"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s uses %s: the admin server must never hold the private key", f, banned)
			}
		}
	}
}
