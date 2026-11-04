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
	"cistern/internal/runner"
	"cistern/internal/server"
	"cistern/web"
)

var version = "dev"

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

var (
	errUsage = errors.New("usage")

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
  backup    Run a backup now                  --job J | --all
  restore   Restore a snapshot                --job J [--snapshot S] [--set k=v]... [--yes]
  list      List snapshots                    --job J [--json]
  verify    Verify snapshots                  --job J [--snapshot S|all] [--json]
  prune     Apply retention                   --job J | --all [--dry-run] [--json]
  check     Check tools, source, repository   --job J | --all
  daemon    Run schedules, serve HTTP         [--listen ADDR] [--grace 5m]
  server    Serve the Admin UI and its API    [--listen ADDR] [--insecure-dev-login]
  drivers   List drivers
  keygen    Create an age X25519 key          [--out FILE]
  version   Print the version

Every command accepts -c FILE or --config FILE (default: $CISTERN_CONFIG, then /etc/cistern/cistern.yaml).
Set CISTERN_LOG_FORMAT=json for JSON logs. Exit codes: 0 success, 1 failure, 2 usage error.
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
	fs.StringVar(c.cfg, "config", "", "config file (same as -c)")

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
		out := fs.String("out", "", "write the private key to FILE (mode 0600, never overwritten)")
		if err = fs.Parse(rest); err == nil {
			err = c.keygen(*out)
		}
	case "daemon":
		listen := fs.String("listen", "", "address for /metrics, /healthz and /status (default: metrics.listen, :9464); the internal API uses daemon.api_listen")
		grace := fs.Duration("grace", 5*time.Minute, "how long to wait for running jobs on shutdown")
		if err = fs.Parse(rest); err == nil {
			err = c.daemon(*listen, *grace)
		}
	case "server":
		listen := fs.String("listen", "", "HTTP(S) address (default: server.listen, 127.0.0.1:8443)")
		dev := fs.Bool("insecure-dev-login", false, "enable /api/v1/auth/dev-login (loopback, no TLS, local development only)")
		if err = fs.Parse(rest); err == nil {
			err = c.server(*listen, *dev)
		}
	case "backup", "check":
		job := fs.String("job", "", "job name")
		all := fs.Bool("all", false, "all jobs")
		if err = fs.Parse(rest); err == nil {
			if err = exactlyOne(*job, *all); err == nil {
				err = c.forJobs(cmd, *job, *all)
			}
		}
	case "prune":
		job := fs.String("job", "", "job name")
		all := fs.Bool("all", false, "all jobs")
		dryRun := fs.Bool("dry-run", false, "show what would be removed, remove nothing")
		asJSON := fs.Bool("json", false, "JSON output")
		if err = fs.Parse(rest); err == nil {
			if err = exactlyOne(*job, *all); err == nil {
				err = c.prune(*job, *all, *dryRun, *asJSON)
			}
		}
	case "list":
		job := fs.String("job", "", "job name (required)")
		asJSON := fs.Bool("json", false, "JSON output")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: --job is required", errUsage)
			} else {
				err = c.list(*job, *asJSON)
			}
		}
	case "verify":
		job := fs.String("job", "", "job name (required)")
		snap := fs.String("snapshot", "latest", "latest, a snapshot ID, a unique ID prefix, or all")
		asJSON := fs.Bool("json", false, "JSON output")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: --job is required", errUsage)
			} else {
				err = c.verify(*job, *snap, *asJSON)
			}
		}
	case "restore":
		job := fs.String("job", "", "job name (required)")
		snap := fs.String("snapshot", "latest", "latest, a snapshot ID or a unique ID prefix")
		var sets kvFlag
		fs.Var(&sets, "set", "override a job parameter, k=v (repeatable)")
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		if err = fs.Parse(rest); err == nil {
			if *job == "" {
				err = fmt.Errorf("%w: --job is required", errUsage)
			} else {
				err = c.restore(*job, *snap, sets, *yes)
			}
		}
	default:
		fmt.Fprintf(stderr, "cistern: unknown command %q\n\n%s", cmd, usage)
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
		return fmt.Errorf("%w: exactly one of --job or --all is required", errUsage)
	}
	return nil
}

func (c *cli) logger() *slog.Logger {
	var h slog.Handler = slog.NewTextHandler(c.stderr, nil)
	if os.Getenv("CISTERN_LOG_FORMAT") == "json" {
		h = slog.NewJSONHandler(c.stderr, nil)
	}
	return slog.New(h)
}

func (c *cli) engine() (*engine.Engine, error) { return c.engineFor(config.ProcessCLI) }

func (c *cli) engineFor(p config.Process) (*engine.Engine, error) {
	cfg, err := config.LoadFor(config.Path(*c.cfg), p)
	if err != nil {
		return nil, err
	}
	e, err := engine.New(cfg, version, c.logger())
	if err != nil {
		return nil, err
	}
	e.Publish = true
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
			fmt.Fprintf(c.stdout, "%s: kept %d, %s %d, errors %d, run records %s %d\n", rep.Job, rep.Kept, verb, rep.Removed, rep.Errors, verb, rep.RunRecordsRemoved)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%w: prune failed for %d of %d jobs", errReported, failed, len(names))
	}
	return nil
}

func (c *cli) server(listen string, dev bool) error {
	cfg, err := config.LoadFor(config.Path(*c.cfg), config.ProcessServer)
	if err != nil {
		return err
	}
	srv, err := server.New(server.Options{Config: cfg, Version: version, Listen: listen, DevLogin: dev, Log: c.logger(), AuditOut: c.stdout, UI: web.FS()})
	if err != nil {
		return err
	}
	return srv.Run(c.ctx)
}

func (c *cli) daemon(listen string, grace time.Duration) error {
	e, err := c.engineFor(config.ProcessDaemon)
	if err != nil {
		return err
	}
	e.Publish = false
	if listen == "" {
		listen = e.Cfg.Metrics.Listen
	}
	var tasks []daemon.Task
	for _, j := range e.Cfg.Jobs {
		for op, expr := range map[string]string{"backup": j.Schedule, "verify": j.VerifySchedule} {
			if expr == "" {
				continue
			}
			s, err := cron.Parse(expr)
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
	dc := e.Cfg.Daemon
	if dc.APIToken != "" {
		e.Log.Info("internal API enabled for the admin server", "listen", dc.APIListen, "path", "/internal/v1/runs", "tls", dc.APITLS.CertFile != "")
	}
	return daemon.Run(c.ctx, daemon.Options{
		Listen:      listen,
		Grace:       grace,
		Metrics:     e.Metrics.Handler(),
		Status:      e.Status,
		Tasks:       tasks,
		Location:    e.Cfg.Location(),
		Log:         e.Log,
		APIToken:    dc.APIToken,
		APIListen:   dc.APIListen,
		APICertFile: e.Cfg.Resolve(dc.APITLS.CertFile),
		APIKeyFile:  e.Cfg.Resolve(dc.APITLS.KeyFile),
		Prepare:     func(req *daemon.RunRequest) error { return runner.Prepare(e.Cfg, req) },
		Execute: func(ctx context.Context, req *daemon.RunRequest) (daemon.Result, error) {
			return runner.Execute(ctx, e, req)
		},
	})
}

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
		return fmt.Errorf("--set needs k=v, got %q", v)
	}
	*k = append(*k, v)
	return nil
}
