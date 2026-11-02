package driver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Command là một lệnh ngoài. Mật khẩu đi qua Env (hoặc file tạm 0600), không bao giờ qua Args.
type Command struct {
	Name   string
	Args   []string
	Env    []string // KEY=VALUE, thêm vào môi trường hiện tại
	Stdin  io.Reader
	Stdout io.Writer
}

func (c Command) String() string { return c.Name + " " + strings.Join(c.Args, " ") }

// Runner cho phép driver thay lệnh thật bằng bản giả trong test.
type Runner func(ctx context.Context, c Command) error

const (
	stderrTail = 4 << 10
	killDelay  = 30 * time.Second
)

// Run chạy lệnh tới khi xong hoặc ctx bị hủy. Khi hủy: SIGTERM cả process group, sau 30 giây SIGKILL.
// Lỗi kèm 4 KiB stderr cuối. Lỗi ghi Stdout (vd. upload hỏng) hủy lệnh và được trả về ưu tiên.
func Run(parent context.Context, c Command) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = c.Stdin
	var out *guardWriter
	if c.Stdout != nil {
		out = &guardWriter{w: c.Stdout, cancel: cancel}
		cmd.Stdout = out
	}
	tail := &tailBuffer{max: stderrTail}
	cmd.Stderr = tail
	setProcessGroup(cmd)
	cmd.WaitDelay = killDelay

	err := cmd.Run()
	killProcessGroup(cmd)
	if out != nil && out.err != nil {
		return fmt.Errorf("%s: writing output: %w", c.Name, out.err)
	}
	if err == nil {
		return nil
	}
	if perr := parent.Err(); perr != nil {
		err = fmt.Errorf("%w (%v)", perr, err)
	}
	if s := strings.TrimSpace(tail.String()); s != "" {
		return fmt.Errorf("%s: %w: %s", c.Name, err, s)
	}
	return fmt.Errorf("%s: %w", c.Name, err)
}

// Output chạy lệnh và trả về stdout (đã bỏ khoảng trắng hai đầu).
func Output(ctx context.Context, run Runner, c Command) (string, error) {
	var buf bytes.Buffer
	c.Stdout = &buf
	if err := run(ctx, c); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

type guardWriter struct {
	w      io.Writer
	cancel context.CancelFunc
	err    error
}

func (g *guardWriter) Write(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n, err := g.w.Write(p)
	if err != nil {
		g.err = err
		g.cancel()
	}
	return n, err
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
