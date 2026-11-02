// Package metrics: registry Prometheus tối giản, đẩy Pushgateway, ghi textfile cho node_exporter.
//
// Mọi metric theo job mang label `cistern_job` (không dùng `job` vì trùng label của
// Prometheus/Pushgateway).
package metrics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const JobLabel = "cistern_job"

const (
	BackupLastSuccess  = "cistern_backup_last_success_timestamp_seconds"
	BackupLastFailure  = "cistern_backup_last_failure_timestamp_seconds"
	BackupFailures     = "cistern_backup_failures_total"
	BackupDuration     = "cistern_backup_duration_seconds"
	BackupLastSize     = "cistern_backup_last_size_bytes"
	BackupPreviousSize = "cistern_backup_previous_size_bytes"
	Snapshots          = "cistern_snapshots"
	PruneRemoved       = "cistern_prune_removed_snapshots_total"
	PruneLastFailure   = "cistern_prune_last_failure_timestamp_seconds"
	VerifyLastSuccess  = "cistern_verify_last_success_timestamp_seconds"
	VerifyLastFailure  = "cistern_verify_last_failure_timestamp_seconds"
	VerifyFailures     = "cistern_verify_failures_total"
	BuildInfo          = "cistern_build_info"
)

type def struct{ typ, help string }

var defs = map[string]def{
	BackupLastSuccess:  {"gauge", "Unix time the last successful backup finished."},
	BackupLastFailure:  {"gauge", "Unix time of the last failed backup."},
	BackupFailures:     {"counter", "Failed backups since this process started."},
	BackupDuration:     {"gauge", "Duration of the last successful backup in seconds."},
	BackupLastSize:     {"gauge", "Stored size in bytes of the newest snapshot."},
	BackupPreviousSize: {"gauge", "Stored size in bytes of the snapshot before the newest."},
	Snapshots:          {"gauge", "Completed snapshots in the repository."},
	PruneRemoved:       {"counter", "Snapshots removed by prune since this process started."},
	PruneLastFailure:   {"gauge", "Unix time of the last failed prune."},
	VerifyLastSuccess:  {"gauge", "Unix time of the last successful verify or restore."},
	VerifyLastFailure:  {"gauge", "Unix time of the last failed verify or restore."},
	VerifyFailures:     {"counter", "Artifacts that failed verification since this process started."},
	BuildInfo:          {"gauge", "Cistern build information."},
}

// Labels là cặp tên/giá trị, giữ thứ tự khi in.
type Labels [][2]string

func Job(name string) Labels { return Labels{{JobLabel, name}} }

func (l Labels) String() string {
	if len(l) == 0 {
		return ""
	}
	parts := make([]string, len(l))
	for i, kv := range l {
		parts[i] = kv[0] + `="` + escape(kv[1]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (l Labels) get(name string) string {
	for _, kv := range l {
		if kv[0] == name {
			return kv[1]
		}
	}
	return ""
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

type sample struct {
	labels Labels
	value  float64
}

type Registry struct {
	mu      sync.Mutex
	samples map[string]map[string]*sample // tên → chuỗi label → mẫu
}

func NewRegistry() *Registry { return &Registry{samples: map[string]map[string]*sample{}} }

func (r *Registry) entry(name string, l Labels) *sample {
	if _, ok := defs[name]; !ok {
		panic("metrics: undeclared metric " + name)
	}
	m := r.samples[name]
	if m == nil {
		m = map[string]*sample{}
		r.samples[name] = m
	}
	k := l.String()
	s := m[k]
	if s == nil {
		s = &sample{labels: l}
		m[k] = s
	}
	return s
}

func (r *Registry) Set(name string, l Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entry(name, l).value = v
}

func (r *Registry) Add(name string, l Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entry(name, l).value += v
}

// Get trả về giá trị và có tồn tại hay không.
func (r *Registry) Get(name string, l Labels) (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.samples[name][l.String()]; ok {
		return s.value, true
	}
	return 0, false
}

// WriteText in theo Prometheus text format 0.0.4. job != "" chỉ in mẫu của job đó.
func (r *Registry) WriteText(w io.Writer, job string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.samples))
	for n := range r.samples {
		names = append(names, n)
	}
	sort.Strings(names)
	bw := bufio.NewWriter(w)
	for _, n := range names {
		var lines []string
		for k, s := range r.samples[n] {
			if job != "" && s.labels.get(JobLabel) != job {
				continue
			}
			lines = append(lines, n+k+" "+formatValue(s.value))
		}
		if len(lines) == 0 {
			continue
		}
		sort.Strings(lines)
		fmt.Fprintf(bw, "# HELP %s %s\n# TYPE %s %s\n", n, defs[n].help, n, defs[n].typ)
		for _, l := range lines {
			bw.WriteString(l + "\n")
		}
	}
	return bw.Flush()
}

func formatValue(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Handler phục vụ /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteText(w, "")
	})
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	},
}

// Push gửi mẫu của job lên Pushgateway bằng POST vào nhóm job=cistern, instance=<job>.
// POST chỉ thay metric cùng tên, nên thời điểm thành công cũ được giữ khi lần sau lỗi.
func (r *Registry) Push(ctx context.Context, gatewayURL, job string) error {
	var body bytes.Buffer
	if err := r.WriteText(&body, job); err != nil {
		return err
	}
	if body.Len() == 0 {
		return nil
	}
	u := strings.TrimRight(gatewayURL, "/") + "/metrics/job/cistern/instance/" + url.PathEscape(job)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pushgateway: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// WriteTextfile ghi <dir>/cistern_<job>.prom (ghi file tạm rồi rename). Mẫu đã có trong file mà
// lần này không đặt lại (vd. thời điểm thành công cũ khi lần này lỗi) được giữ nguyên.
func (r *Registry) WriteTextfile(dir, job string) error {
	path := filepath.Join(dir, "cistern_"+job+".prom")
	merged := map[string]map[string]string{} // tên → "tên{labels}" → dòng
	if old, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(old), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			series, _, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			name, _, _ := strings.Cut(series, "{")
			if _, known := defs[name]; !known {
				continue
			}
			if merged[name] == nil {
				merged[name] = map[string]string{}
			}
			merged[name][series] = line
		}
	}
	var cur bytes.Buffer
	if err := r.WriteText(&cur, job); err != nil {
		return err
	}
	for _, line := range strings.Split(cur.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, _, _ := strings.Cut(line, " ")
		name, _, _ := strings.Cut(series, "{")
		if merged[name] == nil {
			merged[name] = map[string]string{}
		}
		merged[name][series] = line
	}

	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names)
	var out bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&out, "# HELP %s %s\n# TYPE %s %s\n", n, defs[n].help, n, defs[n].typ)
		lines := make([]string, 0, len(merged[n]))
		for _, l := range merged[n] {
			lines = append(lines, l)
		}
		sort.Strings(lines)
		for _, l := range lines {
			out.WriteString(l + "\n")
		}
	}
	tmp, err := os.CreateTemp(dir, ".cistern_"+job+"-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
