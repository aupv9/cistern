// Command cistern: backup, restore, list, verify, prune, check, daemon, drivers, keygen, version.
// Mã thoát: 0 thành công, 1 thao tác lỗi, 2 sai cú pháp.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"cistern/internal/config"
	"cistern/internal/cron"
	"cistern/internal/crypt"
	"cistern/internal/daemon"
	"cistern/internal/driver"
	_ "cistern/internal/driver/all"
	"cistern/internal/engine"
)

var version = "dev" // ghi đè bằng -ldflags "-X main.version=..."

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

var (
	errUsage = errors.New("usage")
	// errReported: lỗi đã in chi tiết (vd. verify), chỉ cần mã thoát 1.
	errReported = errors.New("failed")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const usage = `Usage: cistern <command> [flags]

Commands:
  backup    Chạy backup ngay            (--job J | --all)
  restore   Khôi phục snapshot          --job J [--snapshot S] [--set k=v]... [--yes]
  list      Liệt kê snapshot            --job J [--json]
  verify    Kiểm tra snapshot           --job J [--snapshot S|all] [--json]
  prune     Áp retention                (--job J | --all) [--dry-run] [--json]
  check     Kiểm tra trước khi chạy     (--job J | --all)
  daemon    Lập lịch và HTTP            [--listen :9464] [--grace 5m]
  drivers   Liệt kê driver
  keygen    Tạo khóa X25519             [--out FILE]
  version   In phiên bản

Global: -c FILE (mặc định $CISTERN_CONFIG hoặc /etc/cistern/cistern.yaml)
Log: CISTERN_LOG_FORMAT=json để in log JSON.
`

type cli struct {
	ctx    context.Context
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	cfg    *string
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("cistern "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := &cli{ctx: ctx, stdin: stdin, stdout: stdout, stderr: stderr, cfg: fs.String("c", "", "config file")}

	var err error
	switch cmd {
	case "version":
		if err = fs.Parse(rest); err == nil {
			fmt.Fprintln(stdout, "cistern", version)
		}
	case "drivers":
		if err = fs.Parse(rest); err == nil {
			for _, n := range driver.Names() {
				fmt.Fprintln(stdout, n)
			}
		}
	case "keygen":
		out := fs.String("out", "", "ghi private key vào file (quyền 0600, không ghi đè)")
		if err = fs.Parse(rest); err == nil {
			err = c.keygen(*out)
		}
	case "daemon":
		listen := fs.String("listen", "", "địa chỉ HTTP (mặc định metrics.listen trong cấu hình, :9464)")
		grace := fs.Duration("grace", 5*time.Minute, "thời gian chờ job khi dừng")
		if err = fs.Parse(rest); err == nil {
			err = c.daemon(*listen, *grace)
		}
	case "backup", "check":
		job := fs.String("job", "", "tên job")
		all := fs.Bool("all", false, "mọi job")
		if err = fs.Parse(rest); err == nil {
			if err = exactlyOne(*job, *all); err == nil {
				err = c.forJobs(cmd, *job, *all)
			}
		}
	case "prune":
		job := fs.String("job", "", "tên job")
		all := fs.Bool("all", false, "mọi job")
		dryRun := fs.Bool("dry-run", false, "chỉ in, không xóa")
		asJSON := fs.Bool("json", false, "xuất JSON")
		if err = fs.Parse(rest); err == nil {
			if err = exactlyOne(*job, *all); err == nil {
				err = c.prune(*job, *all, *dryRun, *asJSON)
			}
		}
	case "list":
		job := fs.String("job", "", "tên job (bắt buộc)")
		asJSON := fs.Bool("json", false, "xuất JSON")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: thiếu --job", errUsage)
			} else {
				err = c.list(*job, *asJSON)
			}
		}
	case "verify":
		job := fs.String("job", "", "tên job (bắt buộc)")
		snap := fs.String("snapshot", "latest", "latest, ID, tiền tố ID, hoặc all")
		asJSON := fs.Bool("json", false, "xuất JSON")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: thiếu --job", errUsage)
			} else {
				err = c.verify(*job, *snap, *asJSON)
			}
		}
	case "restore":
		job := fs.String("job", "", "tên job (bắt buộc)")
		snap := fs.String("snapshot", "latest", "latest, ID đầy đủ hoặc tiền tố duy nhất")
		var sets kvFlag
		fs.Var(&sets, "set", "ghi đè tham số job, k=v (lặp lại được)")
		yes := fs.Bool("yes", false, "không hỏi xác nhận")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: thiếu --job", errUsage)
			} else {
				err = c.restore(*job, *snap, sets, *yes)
			}
		}
	default:
		fmt.Fprintf(stderr, "cistern: lệnh không hợp lệ %q\n\n%s", cmd, usage)
		return exitUsage
	}

	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.Is(err, errUsage) || isFlagErr(err):
		fmt.Fprintln(stderr, "cistern:", err)
		return exitUsage
	case errors.Is(err, errReported):
		return exitFail
	default:
		fmt.Fprintln(stderr, "cistern:", err)
		return exitFail
	}
}

func exactlyOne(job string, all bool) error {
	if (job == "") == !all {
		return fmt.Errorf("%w: cần đúng một trong --job hoặc --all", errUsage)
	}
	return nil
}

func (c *cli) engine() (*engine.Engine, error) {
	cfg, err := config.Load(config.Path(*c.cfg))
	if err != nil {
		return nil, err
	}
	var h slog.Handler = slog.NewTextHandler(c.stderr, nil)
	if os.Getenv("CISTERN_LOG_FORMAT") == "json" {
		h = slog.NewJSONHandler(c.stderr, nil)
	}
	e, err := engine.New(cfg, version, slog.New(h))
	if err != nil {
		return nil, err
	}
	e.Publish = true // chạy một lần: đẩy Pushgateway / ghi textfile nếu được cấu hình
	return e, nil
}

func (c *cli) jobNames(e *engine.Engine, job string, all bool) ([]string, error) {
	if !all {
		return []string{job}, nil
	}
	var names []string
	for _, j := range e.Cfg.Jobs {
		names = append(names, j.Name)
	}
	if len(names) == 0 {
		return nil, errors.New("no jobs configured")
	}
	return names, nil
}

func (c *cli) prune(job string, all, dryRun, asJSON bool) error {
	e, err := c.engine()
	if err != nil {
		return err
	}
	names, err := c.jobNames(e, job, all)
	if err != nil {
		return err
	}
	var reports []*engine.PruneReport
	failed := 0
	for _, n := range names {
		rep, err := e.Prune(c.ctx, n, dryRun)
		if err != nil {
			fmt.Fprintf(c.stderr, "cistern: prune %s: %v\n", n, err)
			failed++
			continue
		}
		if rep.Errors > 0 {
			failed++
		}
		reports = append(reports, rep)
	}
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if reports == nil {
			reports = []*engine.PruneReport{}
		}
		if err := enc.Encode(reports); err != nil {
			return err
		}
	} else {
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "JOB\tSNAPSHOT\tFINISHED\tACTION\tREASON")
		for _, rep := range reports {
			for _, it := range rep.Items {
				action := it.Action
				if dryRun && action != engine.ActionKeep {
					action = "would " + action
				}
				if it.Error != "" {
					action += " FAILED: " + it.Error
				}
				finished := "-"
				if !it.FinishedAt.IsZero() {
					finished = it.FinishedAt.Local().Format("2006-01-02 15:04:05")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", rep.Job, it.Snapshot, finished, action, strings.Join(it.Reasons, ","))
			}
		}
		tw.Flush()
		for _, rep := range reports {
			verb := "removed"
			if dryRun {
				verb = "would remove"
			}
			fmt.Fprintf(c.stdout, "%s: kept %d, %s %d, errors %d\n", rep.Job, rep.Kept, verb, rep.Removed, rep.Errors)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%w: prune failed for %d of %d jobs", errReported, failed, len(names))
	}
	return nil
}

func (c *cli) daemon(listen string, grace time.Duration) error {
	e, err := c.engine()
	if err != nil {
		return err
	}
	e.Publish = false // daemon phục vụ /metrics
	if listen == "" {
		listen = e.Cfg.Metrics.Listen
	}
	var tasks []daemon.Task
	for _, j := range e.Cfg.Jobs {
		for op, expr := range map[string]string{"backup": j.Schedule, "verify": j.VerifySchedule} {
			if expr == "" {
				continue
			}
			s, err := cron.Parse(expr) // đã kiểm khi nạp cấu hình
			if err != nil {
				return fmt.Errorf("job %s: %w", j.Name, err)
			}
			tasks = append(tasks, daemon.Task{Job: j.Name, Op: op, Expr: expr, Schedule: s})
		}
	}
	if len(tasks) == 0 {
		e.Log.Warn("no job has schedule or verify_schedule: daemon only serves /metrics, /healthz and /status")
	}
	e.RefreshMetrics(c.ctx)
	return daemon.Run(c.ctx, daemon.Options{
		Listen:   listen,
		Grace:    grace,
		Metrics:  e.Metrics.Handler(),
		Status:   e.Status,
		Tasks:    tasks,
		Location: e.Cfg.Location(),
		Log:      e.Log,
		Runner: func(ctx context.Context, job, op string) error {
			switch op {
			case "backup":
				_, err := e.Backup(ctx, job)
				return err
			case "verify":
				rep, err := e.Verify(ctx, job, "latest")
				if err == nil && rep.Failures > 0 {
					err = fmt.Errorf("verify: %d artifact(s) failed", rep.Failures)
				}
				return err
			}
			return fmt.Errorf("unknown op %q", op)
		},
	})
}

// forJobs chạy backup/check cho một job hoặc tất cả (lần lượt, tiếp tục khi có job lỗi).
func (c *cli) forJobs(op, job string, all bool) error {
	e, err := c.engine()
	if err != nil {
		return err
	}
	var names []string
	names, err = c.jobNames(e, job, all)
	if err != nil {
		return err
	}
	failed := 0
	for _, n := range names {
		switch op {
		case "backup":
			m, err := e.Backup(c.ctx, n)
			if err != nil {
				fmt.Fprintf(c.stderr, "cistern: %v\n", err)
				failed++
				continue
			}
			fmt.Fprintf(c.stdout, "%s\t%s\n", n, m.ID)
		case "check":
			if err := e.Check(c.ctx, n); err != nil {
				fmt.Fprintf(c.stdout, "%s\tFAIL\t%v\n", n, err)
				failed++
				continue
			}
			fmt.Fprintf(c.stdout, "%s\tOK\n", n)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%w: %d of %d jobs", errReported, failed, len(names))
	}
	return nil
}

func (c *cli) list(job string, asJSON bool) error {
	e, err := c.engine()
	if err != nil {
		return err
	}
	list, err := e.List(c.ctx, job)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if list == nil {
			return enc.Encode([]any{})
		}
		return enc.Encode(list)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SNAPSHOT\tFINISHED\tDURATION\tARTIFACTS\tSTORED\tSOURCE")
	for _, m := range list {
		var stored int64
		for _, a := range m.Artifacts {
			stored += a.StoredSize
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s %s\n", m.ID, m.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			m.FinishedAt.Sub(m.StartedAt).Round(time.Second), len(m.Artifacts), humanBytes(stored), m.Driver, m.Source.Version)
	}
	return tw.Flush()
}

func (c *cli) verify(job, snap string, asJSON bool) error {
	e, err := c.engine()
	if err != nil {
		return err
	}
	rep, err := e.Verify(c.ctx, job, snap)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SNAPSHOT\tARTIFACT\tMODE\tRESULT")
		for _, r := range rep.Results {
			res := "OK"
			if !r.OK {
				res = "FAIL: " + r.Error
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Snapshot, r.Artifact, r.Mode, res)
		}
		tw.Flush()
	}
	if rep.Failures > 0 {
		fmt.Fprintf(c.stderr, "cistern: verify: %d failure(s)\n", rep.Failures)
		return errReported
	}
	return nil
}

func (c *cli) restore(job, snap string, sets kvFlag, yes bool) error {
	overrides := map[string]string{}
	for _, kv := range sets {
		k, v, _ := strings.Cut(kv, "=")
		overrides[k] = v
	}
	e, err := c.engine()
	if err != nil {
		return err
	}
	if _, err := e.Cfg.Job(job); err != nil {
		return err
	}
	if !yes {
		f, ok := c.stdin.(*os.File)
		if !ok || !isTerminal(f) {
			return errors.New("restore needs --yes when not running in a terminal")
		}
		fmt.Fprintf(c.stderr, "Restore job %q from snapshot %q", job, snap)
		if len(overrides) > 0 {
			fmt.Fprintf(c.stderr, " with %s", strings.Join(sets, " "))
		}
		fmt.Fprintf(c.stderr, ".\nType the job name to confirm: ")
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if strings.TrimSpace(line) != job {
			return errors.New("confirmation did not match, aborting")
		}
	}
	m, err := e.Restore(c.ctx, job, snap, overrides)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "restored %s from %s\n", job, m.ID)
	return nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (c *cli) keygen(out string) error {
	id, err := crypt.GenerateIdentity()
	if err != nil {
		return err
	}
	if out == "" {
		return crypt.WriteIdentityFile(c.stdout, id, time.Now())
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := crypt.WriteIdentityFile(f, id, time.Now()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "Private key written to %s. Keep it outside the backup system.\n", out)
	fmt.Fprintln(c.stdout, id.Recipient())
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func isFlagErr(err error) bool {
	s := err.Error()
	return strings.HasPrefix(s, "flag provided but not defined") || strings.HasPrefix(s, "invalid ")
}

type kvFlag []string

func (k *kvFlag) String() string { return strings.Join(*k, ",") }
func (k *kvFlag) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("--set cần dạng k=v, nhận %q", v)
	}
	*k = append(*k, v)
	return nil
}
