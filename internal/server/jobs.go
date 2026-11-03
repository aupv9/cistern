package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/manifest"
	"cistern/internal/retention"
)

const (
	staleAfter       = 36 * time.Hour
	verifyStaleAfter = 8 * 24 * time.Hour
	recentPoints     = 30
)

const (
	HealthHealthy       = "healthy"
	HealthFailing       = "failing"
	HealthRunning       = "running"
	HealthStale         = "stale"
	HealthVerifyOverdue = "verify_overdue"
	HealthNeverRun      = "never_run"
)

type runPoint struct {
	ID         string    `json:"id,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS *int64    `json:"duration_ms,omitempty"`
}

type alertView struct {
	Kind    string    `json:"kind"`
	Job     string    `json:"job,omitempty"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

type jobSummary struct {
	Name             string     `json:"name"`
	Driver           string     `json:"driver"`
	SourceLabel      string     `json:"source_label,omitempty"`
	Repository       string     `json:"repository"`
	Schedule         string     `json:"schedule,omitempty"`
	VerifySchedule   string     `json:"verify_schedule,omitempty"`
	Health           string     `json:"health"`
	RunningOp        string     `json:"running_op,omitempty"`
	LastSuccess      *time.Time `json:"last_success,omitempty"`
	LastFailure      *time.Time `json:"last_failure,omitempty"`
	LastFailureError string     `json:"last_failure_error,omitempty"`
	LastVerify       *time.Time `json:"last_verify,omitempty"`
	NextRun          *time.Time `json:"next_run,omitempty"`
	Snapshots        int        `json:"snapshots"`
	StoredBytes      int64      `json:"stored_bytes"`
	RecentBackups    []runPoint `json:"recent_backups"`
}

type objectLockView struct {
	Mode string `json:"mode"`
	Days int    `json:"days"`
}

type retentionView struct {
	Last    int `json:"last,omitempty"`
	Hourly  int `json:"hourly,omitempty"`
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
	Yearly  int `json:"yearly,omitempty"`
}

type targetView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Production  bool   `json:"production"`
}

type encryptionView struct {
	Mode       string `json:"mode"`
	Recipients int    `json:"recipients"`
}

type jobDetail struct {
	jobSummary
	Retention        retentionView     `json:"retention"`
	PruneAfterBackup bool              `json:"prune_after_backup"`
	TimeoutMS        int64             `json:"timeout_ms"`
	ObjectLock       *objectLockView   `json:"object_lock,omitempty"`
	Encryption       encryptionView    `json:"encryption"`
	Source           map[string]string `json:"source"`
	RestoreTargets   []targetView      `json:"restore_targets"`
	CurrentRun       *admin.Run        `json:"current_run,omitempty"`
}

type lastVerifyView struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

type snapshotSummary struct {
	ID          string          `json:"id"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	DurationMS  int64           `json:"duration_ms"`
	StoredBytes int64           `json:"stored_bytes"`
	PlainBytes  int64           `json:"plain_bytes"`
	Artifacts   int             `json:"artifacts"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	KeptBy      []string        `json:"kept_by"`
	LockUntil   *time.Time      `json:"lock_until,omitempty"`
	LastVerify  *lastVerifyView `json:"last_verify,omitempty"`
}

type repoView struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	StoredBytes int64           `json:"stored_bytes"`
	ObjectLock  *objectLockView `json:"object_lock,omitempty"`
}

type overviewResponse struct {
	GeneratedAt      time.Time      `json:"generated_at"`
	Jobs             []jobSummary   `json:"jobs"`
	Alerts           []alertView    `json:"alerts"`
	PendingApprovals []approvalView `json:"pending_approvals"`
	RecentActivity   []*AuditEntry  `json:"recent_activity"`
	Repositories     []repoView     `json:"repositories"`
}

var driverNames = map[string]string{"postgres": "PostgreSQL", "mysql": "MySQL", "s3": "S3"}

func sizes(m *manifest.Manifest) (stored, plain int64) {
	for _, a := range m.Artifacts {
		stored += a.StoredSize
		plain += a.PlainSize
	}
	return stored, plain
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t.UTC()
	return &v
}

func isVerifyOp(op string) bool { return op == admin.OpVerify || op == admin.OpRestore }

func (s *Server) summarize(job *config.Job, d *jobData, dv daemonView, now time.Time) (jobSummary, []alertView) {
	sum := jobSummary{
		Name: job.Name, Driver: job.Driver, Repository: job.Repository, Schedule: job.Schedule,
		VerifySchedule: job.VerifySchedule, Snapshots: len(d.snaps), RecentBackups: []runPoint{},
	}
	var lastSuccess, lastFailure, lastVerify, lastVerifyFail, firstSuccess time.Time
	var failErr, verifyErr string
	covered := map[string]bool{}
	var points []runPoint
	for _, m := range d.snaps {
		st, _ := sizes(m)
		sum.StoredBytes += st
		if m.FinishedAt.After(lastSuccess) {
			lastSuccess = m.FinishedAt
		}
		if firstSuccess.IsZero() || m.FinishedAt.Before(firstSuccess) {
			firstSuccess = m.FinishedAt
		}
	}
	if n := len(d.snaps); n > 0 {
		m := d.snaps[n-1]
		name := driverNames[m.Driver]
		if name == "" {
			name = m.Driver
		}
		sum.SourceLabel = strings.TrimSpace(name + " " + safeText(m.Source.Version, 40))
	}
	for _, r := range d.runs {
		switch {
		case r.Op == admin.OpBackup:
			dur := r.DurationMS
			points = append(points, runPoint{ID: r.ID, Status: r.Status, StartedAt: r.StartedAt.UTC(), DurationMS: &dur})
			if r.Snapshot != "" {
				covered[r.Snapshot] = true
			}
			if r.Status == admin.StateSucceeded && r.FinishedAt.After(lastSuccess) {
				lastSuccess = r.FinishedAt
			}
			if r.Status == admin.StateFailed && r.FinishedAt.After(lastFailure) {
				lastFailure, failErr = r.FinishedAt, r.Error
			}
		case isVerifyOp(r.Op):
			if r.Status == admin.StateSucceeded && r.FinishedAt.After(lastVerify) {
				lastVerify = r.FinishedAt
			}
			if r.Status == admin.StateFailed && r.FinishedAt.After(lastVerifyFail) {
				lastVerifyFail, verifyErr = r.FinishedAt, r.Error
			}
		}
	}
	for _, m := range d.snaps {
		if !covered[m.ID] {
			dur := m.FinishedAt.Sub(m.StartedAt).Milliseconds()
			points = append(points, runPoint{Status: admin.StateSucceeded, StartedAt: m.StartedAt.UTC(), DurationMS: &dur})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].StartedAt.Before(points[j].StartedAt) })
	if len(points) > recentPoints {
		points = points[len(points)-recentPoints:]
	}
	if points != nil {
		sum.RecentBackups = points
	}
	sum.LastSuccess, sum.LastFailure, sum.LastVerify = tptr(lastSuccess), tptr(lastFailure), tptr(lastVerify)
	if !lastFailure.IsZero() {
		sum.LastFailureError = failErr
	}
	sum.NextRun = dv.next(job.Name, admin.OpBackup)
	if ro := dv.running(job.Name); ro != nil {
		sum.RunningOp = ro.Op
	}

	var alerts []alertView
	failing := !lastFailure.IsZero() && lastFailure.After(lastSuccess)
	stale := !lastSuccess.IsZero() && now.Sub(lastSuccess) > staleAfter
	verifyRef := lastVerify
	if verifyRef.IsZero() {
		verifyRef = firstSuccess
	}
	verifyOverdue := job.VerifySchedule != "" && !verifyRef.IsZero() && now.Sub(verifyRef) > verifyStaleAfter
	if failing {
		alerts = append(alerts, alertView{Kind: "backup_failing", Job: job.Name, Since: lastFailure.UTC(), Message: "The last backup failed: " + safeText(failErr, 300)})
	}
	if stale {
		alerts = append(alerts, alertView{Kind: "backup_stale", Job: job.Name, Since: lastSuccess.Add(staleAfter).UTC(), Message: "No successful backup for more than 36 hours"})
	}
	if !lastVerifyFail.IsZero() && lastVerifyFail.After(lastVerify) {
		alerts = append(alerts, alertView{Kind: "verify_failing", Job: job.Name, Since: lastVerifyFail.UTC(), Message: "The last verify or restore failed: " + safeText(verifyErr, 300)})
	}
	if verifyOverdue {
		alerts = append(alerts, alertView{Kind: "verify_stale", Job: job.Name, Since: verifyRef.Add(verifyStaleAfter).UTC(), Message: "No successful verify or restore for more than 8 days"})
	}
	switch {
	case sum.RunningOp != "":
		sum.Health = HealthRunning
	case len(d.snaps) == 0 && lastFailure.IsZero() && lastSuccess.IsZero():
		sum.Health = HealthNeverRun
	case failing:
		sum.Health = HealthFailing
	case stale:
		sum.Health = HealthStale
	case verifyOverdue:
		sum.Health = HealthVerifyOverdue
	default:
		sum.Health = HealthHealthy
	}
	return sum, alerts
}

func (s *Server) objectLock(repoName string) *objectLockView {
	rc := s.cfg.Repositories[repoName]
	if rc == nil || rc.ObjectLock == nil {
		return nil
	}
	return &objectLockView{Mode: rc.ObjectLock.Mode, Days: rc.ObjectLock.Days}
}

func (s *Server) overview(w http.ResponseWriter, rq *request) {
	ctx := rq.Context()
	now := s.now().UTC()
	dv := s.daemon.get(ctx, false)
	all := s.data.all(ctx)
	resp := overviewResponse{GeneratedAt: now, Jobs: []jobSummary{}, Alerts: []alertView{}, PendingApprovals: []approvalView{}, RecentActivity: []*AuditEntry{}}
	repoBytes := map[string]int64{}
	for _, j := range s.cfg.Jobs {
		sum, alerts := s.summarize(j, all[j.Name], dv, now)
		resp.Jobs = append(resp.Jobs, sum)
		resp.Alerts = append(resp.Alerts, alerts...)
		repoBytes[j.Repository] += sum.StoredBytes
	}
	if dv.configured && !dv.reachable {
		resp.Alerts = append(resp.Alerts, alertView{Kind: "daemon_unreachable", Job: "", Since: s.daemon.downSince(), Message: "The daemon cannot be reached: the UI is read-only and schedules may not be running"})
	}
	sort.SliceStable(resp.Alerts, func(i, j int) bool { return resp.Alerts[i].Since.After(resp.Alerts[j].Since) })
	names := make([]string, 0, len(s.cfg.Repositories))
	for n := range s.cfg.Repositories {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		resp.Repositories = append(resp.Repositories, repoView{Name: n, Type: s.cfg.Repositories[n].Type, StoredBytes: repoBytes[n], ObjectLock: s.objectLock(n)})
	}
	if list, err := s.approvals.list(ctx, false); err == nil {
		for _, a := range list {
			if a.state(now) == ApprovalPending {
				resp.PendingApprovals = append(resp.PendingApprovals, s.approvalView(a, rq.sess, now))
			}
		}
	} else {
		s.log.Warn("listing approvals failed", "err", err)
	}
	q := auditQuery{From: now.Add(-7 * 24 * time.Hour), To: now.Add(time.Minute), Limit: 10, Exclude: map[string]bool{ActionLogin: true, ActionLogout: true}}
	if entries, _, err := s.audit.page(ctx, q); err == nil {
		resp.RecentActivity = entries
	} else {
		s.log.Warn("reading recent audit entries failed", "err", err)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) jobFor(w http.ResponseWriter, rq *request) (*config.Job, bool) {
	j, err := s.cfg.Job(rq.PathValue("job"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "job not found", nil)
		return nil, false
	}
	return j, true
}

var sourceAllowlist = []string{"host", "port", "database", "databases", "bucket", "prefix", "endpoint"}

func safeSource(params map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range sourceAllowlist {
		v, ok := params[k]
		if !ok || v == "" {
			continue
		}
		if k == "endpoint" || strings.Contains(v, "://") {
			if u, err := url.Parse(v); err == nil && u.User != nil {
				u.User = nil
				v = u.String()
			}
		}
		out[k] = v
	}
	return out
}

func (s *Server) jobDetail(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	ctx := rq.Context()
	dv := s.daemon.get(ctx, false)
	d := s.data.job(ctx, j)
	if d.err != nil && len(d.snaps) == 0 {
		s.log.Warn("reading job data failed", "job", j.Name, "err", d.err)
	}
	sum, _ := s.summarize(j, d, dv, s.now().UTC())
	det := jobDetail{
		jobSummary: sum, TimeoutMS: j.Timeout.Milliseconds(), ObjectLock: s.objectLock(j.Repository),
		Source: safeSource(j.Params), RestoreTargets: []targetView{},
		Encryption: encryptionView{Mode: "none", Recipients: len(s.cfg.Recipients)},
	}
	if len(s.cfg.Recipients) > 0 {
		det.Encryption.Mode = "age-x25519"
	}
	if j.PruneAfterBackup != nil {
		det.PruneAfterBackup = *j.PruneAfterBackup
	}
	if p := j.Retention; p != nil {
		det.Retention = retentionView{Last: p.Last, Hourly: p.Hourly, Daily: p.Daily, Weekly: p.Weekly, Monthly: p.Monthly, Yearly: p.Yearly}
	}
	for _, t := range j.Targets() {
		det.RestoreTargets = append(det.RestoreTargets, targetView{Name: t.Name, Description: t.Description, Production: t.IsProduction()})
	}
	if ro := dv.running(j.Name); ro != nil && s.client != nil {
		if run, err := s.client.Run(ctx, ro.RunID); err == nil {
			run.LogTail = nil
			det.CurrentRun = run
		}
	}
	writeJSON(w, http.StatusOK, det)
}

func (s *Server) snapshotSummaries(j *config.Job, d *jobData) []snapshotSummary {
	kept := map[string][]string{}
	var rs []retention.Snapshot
	for _, m := range d.snaps {
		rs = append(rs, retention.Snapshot{ID: m.ID, FinishedAt: m.FinishedAt, DependsOn: m.DependsOn})
	}
	if p := j.Retention; p != nil {
		for _, dec := range retention.Apply(retention.Policy{Last: p.Last, Hourly: p.Hourly, Daily: p.Daily, Weekly: p.Weekly, Monthly: p.Monthly, Yearly: p.Yearly}, rs, s.cfg.Location()) {
			if dec.Keep {
				kept[dec.ID] = append([]string{}, dec.Reasons...)
			}
		}
	}
	verified := map[string]*lastVerifyView{}
	for _, r := range d.runs {
		if !isVerifyOp(r.Op) || r.Snapshot == "" {
			continue
		}
		if v := verified[r.Snapshot]; v == nil || r.FinishedAt.After(v.At) {
			verified[r.Snapshot] = &lastVerifyView{Status: r.Status, At: r.FinishedAt.UTC()}
		}
	}
	lock := s.objectLock(j.Repository)
	out := []snapshotSummary{}
	for i := len(d.snaps) - 1; i >= 0; i-- {
		m := d.snaps[i]
		st, pl := sizes(m)
		ss := snapshotSummary{
			ID: m.ID, StartedAt: m.StartedAt.UTC(), FinishedAt: m.FinishedAt.UTC(), DurationMS: m.FinishedAt.Sub(m.StartedAt).Milliseconds(),
			StoredBytes: st, PlainBytes: pl, Artifacts: len(m.Artifacts), DependsOn: m.DependsOn, KeptBy: []string{},
			LastVerify: verified[m.ID],
		}
		if j.Retention == nil {
			ss.KeptBy = []string{"no_policy"}
		} else if k := kept[m.ID]; k != nil {
			ss.KeptBy = k
		}
		if lock != nil {
			ss.LockUntil = tptr(m.FinishedAt.Add(time.Duration(lock.Days) * 24 * time.Hour))
		}
		out = append(out, ss)
	}
	return out
}

func (s *Server) snapshots(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	d := s.data.job(rq.Context(), j)
	if d.err != nil && len(d.snaps) == 0 {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the repository could not be read", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": s.snapshotSummaries(j, d)})
}

func (s *Server) snapshotManifest(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	id := rq.PathValue("snapshot")
	for _, m := range s.data.job(rq.Context(), j).snaps {
		if m.ID == id {
			writeJSON(w, http.StatusOK, m)
			return
		}
	}
	writeError(w, http.StatusNotFound, codeNotFound, "snapshot not found", nil)
}

func (s *Server) snapshotManifestFile(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	id := rq.PathValue("snapshot")
	if !manifest.ValidSnapshotID(id) {
		writeError(w, http.StatusNotFound, codeNotFound, "snapshot not found", nil)
		return
	}
	r, prefix, err := s.data.repos.Open(j.Repository)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the repository could not be opened", nil)
		return
	}
	rc, err := r.Get(rq.Context(), manifest.ManifestKey(prefix, j.Name, id))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "snapshot not found", nil)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Disposition", `attachment; filename="`+j.Name+"-"+id+`-manifest.json"`)
	h.Set("Cache-Control", "no-store")
	io.Copy(w, io.LimitReader(rc, 16<<20))
}

func intParam(rq *request, name string, def, max int) (int, bool) {
	v := rq.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false
	}
	if n > max {
		n = max
	}
	return n, true
}

var validOps = map[string]bool{admin.OpBackup: true, admin.OpVerify: true, admin.OpRestore: true, admin.OpPrune: true}

func (s *Server) jobRuns(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	op := rq.URL.Query().Get("op")
	if op != "" && !validOps[op] {
		invalid(w, map[string]string{"op": "must be backup, verify, restore or prune"})
		return
	}
	limit, ok := intParam(rq, "limit", 30, 200)
	if !ok {
		invalid(w, map[string]string{"limit": "must be a positive integer"})
		return
	}
	ctx := rq.Context()
	d := s.data.job(ctx, j)
	runs := []admin.Run{}
	seen := map[string]bool{}
	for _, r := range d.runs {
		seen[r.ID] = true
	}
	for _, r := range s.liveRuns(ctx, true) {
		if r.Job == j.Name && !seen[r.ID] && (op == "" || r.Op == op) {
			runs = append(runs, r)
		}
	}
	for i := len(d.runs) - 1; i >= 0 && len(runs) < limit; i-- {
		if r := d.runs[i]; op == "" || r.Op == op {
			runs = append(runs, r.Run())
		}
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) liveRuns(ctx context.Context, active bool) []admin.Run {
	if s.client == nil {
		return nil
	}
	dv := s.daemon.get(ctx, false)
	if !dv.reachable {
		return nil
	}
	runs, err := s.client.Runs(ctx, active)
	if err != nil {
		return nil
	}
	return runs
}
