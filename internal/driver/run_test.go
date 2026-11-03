package driver

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunStdoutAndStderrTail(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Command{Name: "sh", Args: []string{"-c", "printf hi"}, Stdout: &out}); err != nil || out.String() != "hi" {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
	err := Run(context.Background(), Command{Name: "sh", Args: []string{"-c", "head -c 10000 /dev/zero | tr '\\0' x >&2; echo LAST >&2; exit 3"}})
	if err == nil || !strings.Contains(err.Error(), "LAST") || len(err.Error()) > stderrTail+200 {
		t.Fatalf("err len %d: %.200v", len(err.Error()), err)
	}
}

func TestRunEnvNotArgs(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Command{Name: "sh", Args: []string{"-c", `printf %s "$SECRET"`}, Env: []string{"SECRET=s3cr3t"}, Stdout: &out})
	if err != nil || out.String() != "s3cr3t" {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestRunTimeoutKillsGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()

	err := Run(ctx, Command{Name: "sh", Args: []string{"-c", "sleep 30 & sleep 30; wait"}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("upload broken") }

func TestRunStdoutErrorCancels(t *testing.T) {
	start := time.Now()
	err := Run(context.Background(), Command{Name: "sh", Args: []string{"-c", "yes"}, Stdout: failWriter{}})
	if err == nil || !strings.Contains(err.Error(), "upload broken") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}
