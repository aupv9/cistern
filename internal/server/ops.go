package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/engine"
	"cistern/internal/manifest"
)

const redacted = "<redacted>"

func (s *Server) requireDaemon(w http.ResponseWriter, ctx context.Context) (daemonView, bool) {
	if s.client == nil {
		writeError(w, http.StatusServiceUnavailable, codeDaemon, "no daemon is configured: the UI is read-only", nil)
		return daemonView{}, false
	}
	dv := s.daemon.get(ctx, true)
	if !dv.reachable {
		if dv.err != nil {
			s.log.Warn("daemon unavailable", "err", dv.err)
		}
		writeError(w, http.StatusServiceUnavailable, codeDaemon, "the daemon cannot be reached: the UI is read-only for now", nil)
		return dv, false
	}
	return dv, true
}

func busy(w http.ResponseWriter, dv daemonView, job string) bool {
	if ro := dv.running(job); ro != nil {
		writeError(w, http.StatusConflict, codeConflict, "job "+job+" already has a running "+ro.Op+" (run "+ro.RunID+")", nil)
		return true
	}
	return false
}

func writeDispatchError(w http.ResponseWriter, err error) {
	var ae *daemon.APIError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusConflict:
		writeError(w, http.StatusConflict, codeConflict, ae.Message, nil)
	case errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		writeError(w, http.StatusUnprocessableEntity, codeInvalid, ae.Message, ae.Fields)
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		writeError(w, http.StatusNotFound, codeNotFound, ae.Message, nil)
	case errors.Is(err, daemon.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, codeDaemon, "the daemon cannot be reached: the UI is read-only for now", nil)
	default:
		writeError(w, http.StatusBadGateway, codeInternal, "the daemon rejected the request", nil)
	}
}

func auditUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, codeInternal, "the audit log cannot be written, so the action was not performed", nil)
}

func (s *Server) dispatch(ctx context.Context, req *daemon.RunRequest) error {
	if _, err := s.client.StartRun(ctx, req); err != nil {
		return err
	}
	s.tracker.add(req)
	s.daemon.mu.Lock()
	s.daemon.fetched = time.Time{}
	s.daemon.mu.Unlock()
	return nil
}

func (s *Server) startBackup(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	ctx := rq.Context()
	dv, ok := s.requireDaemon(w, ctx)
	if !ok || busy(w, dv, j.Name) {
		return
	}
	actor := rq.actor()
	req := &daemon.RunRequest{RunID: admin.NewID(s.now()), Op: admin.OpBackup, Job: j.Name, RequestedBy: &actor}
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionBackup, Result: ResultAccepted, Job: j.Name, RunID: req.RunID,
		Summary: displayName(actor) + " started a backup of " + j.Name}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.dispatch(ctx, req); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionBackup, Result: ResultFailed, Job: j.Name, RunID: req.RunID, Error: dispatchMessage(err),
			Summary: "The backup of " + j.Name + " requested by " + displayName(actor) + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": req.RunID})
}

func dispatchMessage(err error) string {
	var ae *daemon.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	if errors.Is(err, daemon.ErrUnavailable) {
		return "daemon unavailable"
	}
	return "daemon error"
}

func (s *Server) resolveSnapshot(ctx context.Context, j *config.Job, ref string) (*manifest.Manifest, string) {
	d := s.data.job(ctx, j)
	if d.err != nil && len(d.snaps) == 0 {
		return nil, "the repository could not be read"
	}
	m, err := engine.Resolve(d.snaps, ref)
	if err != nil {
		s.data.invalidate(j.Name)
		d = s.data.jobSync(ctx, j)
		if m, err = engine.Resolve(d.snaps, ref); err != nil {
			return nil, err.Error()
		}
	}
	return m, ""
}

func (s *Server) startVerify(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	var in struct {
		Snapshot string `json:"snapshot"`
	}
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	if in.Snapshot == "" {
		in.Snapshot = "latest"
	}
	ctx := rq.Context()
	m, msg := s.resolveSnapshot(ctx, j, in.Snapshot)
	if m == nil {
		invalid(w, map[string]string{"snapshot": msg})
		return
	}
	dv, ok := s.requireDaemon(w, ctx)
	if !ok || busy(w, dv, j.Name) {
		return
	}
	actor := rq.actor()
	req := &daemon.RunRequest{RunID: admin.NewID(s.now()), Op: admin.OpVerify, Job: j.Name, Snapshot: m.ID, RequestedBy: &actor}
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionVerify, Result: ResultAccepted, Job: j.Name, Snapshot: m.ID, RunID: req.RunID,
		Summary: displayName(actor) + " started a verify of " + j.Name + " snapshot " + m.ID}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.dispatch(ctx, req); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionVerify, Result: ResultFailed, Job: j.Name, Snapshot: m.ID, RunID: req.RunID, Error: dispatchMessage(err),
			Summary: "The verify of " + j.Name + " requested by " + displayName(actor) + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": req.RunID})
}

type restoreInput struct {
	Snapshot      string  `json:"snapshot"`
	Target        string  `json:"target"`
	RestoreSuffix *string `json:"restore_suffix"`
	Reason        string  `json:"reason"`
	ConfirmJob    string  `json:"confirm_job"`
}

type resolvedRestore struct {
	snap      *manifest.Manifest
	target    *config.RestoreTarget
	overrides map[string]string
	suffix    string
}

func (s *Server) resolveRestore(ctx context.Context, j *config.Job, in *restoreInput) (*resolvedRestore, map[string]string) {
	fields := map[string]string{}
	res := &resolvedRestore{}
	if in.Snapshot == "" {
		fields["snapshot"] = "required"
	} else if m, msg := s.resolveSnapshot(ctx, j, in.Snapshot); m == nil {
		fields["snapshot"] = msg
	} else {
		res.snap = m
	}
	if in.Target == "" {
		fields["target"] = "required"
	} else {
		ov, t, err := j.RestoreOverrides(in.Target, in.RestoreSuffix)
		switch {
		case errors.Is(err, config.ErrUnknownTarget):
			fields["target"] = "not one of the job's restore targets"
		case errors.Is(err, config.ErrSuffixUnsupported):
			fields["restore_suffix"] = "the " + j.Driver + " driver does not support restore_suffix"
		case errors.Is(err, config.ErrInvalidSuffix):
			fields["restore_suffix"] = "must match ^_[a-z0-9_]{1,30}$"
		case err != nil:
			fields["target"] = err.Error()
		default:
			res.target, res.overrides, res.suffix = t, ov, ov["restore_suffix"]
		}
	}
	if len(fields) > 0 {
		return nil, fields
	}
	return res, nil
}

func maskOverrides(ov map[string]string) map[string]string {
	out := make(map[string]string, len(ov))
	for k, v := range ov {
		if admin.IsSecretKey(k) {
			v = redacted
		}
		out[k] = v
	}
	return out
}

func shellQuote(s string) string {
	safe := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.,/:=@+%", c)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func cliCommand(job, snapshot string, ov map[string]string) string {
	parts := []string{"cistern", "restore", "--job", shellQuote(job), "--snapshot", shellQuote(snapshot)}
	keys := make([]string, 0, len(ov))
	for k := range ov {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, "--set", shellQuote(k+"="+ov[k]))
	}
	return strings.Join(append(parts, "--yes"), " ")
}

type restorePreview struct {
	Job           string            `json:"job"`
	Snapshot      snapshotSummary   `json:"snapshot"`
	Target        targetView        `json:"target"`
	NeedsApproval bool              `json:"needs_approval"`
	Overrides     map[string]string `json:"overrides"`
	CLICommand    string            `json:"cli_command"`
}

func (s *Server) restorePreview(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	var in restoreInput
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	ctx := rq.Context()
	res, fields := s.resolveRestore(ctx, j, &in)
	if fields != nil {
		invalid(w, fields)
		return
	}
	masked := maskOverrides(res.overrides)
	p := restorePreview{
		Job: j.Name, Target: targetView{Name: res.target.Name, Description: res.target.Description, Production: res.target.IsProduction()},
		NeedsApproval: res.target.IsProduction(), Overrides: masked, CLICommand: cliCommand(j.Name, res.snap.ID, masked),
	}
	for _, ss := range s.snapshotSummaries(j, s.data.job(ctx, j)) {
		if ss.ID == res.snap.ID {
			p.Snapshot = ss
		}
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) startRestore(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	var in restoreInput
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	ctx := rq.Context()
	res, fields := s.resolveRestore(ctx, j, &in)
	if fields == nil {
		fields = map[string]string{}
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(in.Reason); n < 3 || n > 500 {
		fields["reason"] = "must be 3 to 500 characters"
	}
	if in.ConfirmJob != j.Name {
		fields["confirm_job"] = "must equal the job name"
	}
	if len(fields) > 0 {
		invalid(w, fields)
		return
	}
	actor := rq.actor()
	now := s.now().UTC()
	if res.target.IsProduction() {
		suffix := res.suffix
		req := &approvalRequest{
			ID: admin.NewID(now), Job: j.Name, Snapshot: res.snap.ID, Target: res.target.Name, RestoreSuffix: &suffix,
			Reason: in.Reason, RequestedBy: actor, RequestedAt: now,
		}
		if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionRestoreRequest, Result: ResultPending, Job: j.Name, Snapshot: res.snap.ID,
			Target: res.target.Name, ApprovalID: req.ID, Reason: in.Reason, Params: paramsWithSuffix(nil, suffix),
			Summary: displayName(actor) + " requested approval to restore " + j.Name + " into production target " + res.target.Name}) != nil {
			auditUnavailable(w)
			return
		}
		if err := s.approvals.create(ctx, req); err != nil {
			s.log.Error("creating approval request failed", "err", err)
			writeError(w, http.StatusInternalServerError, codeInternal, "the approval request could not be stored", nil)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"needs_approval": true, "approval_id": req.ID})
		return
	}
	dv, ok := s.requireDaemon(w, ctx)
	if !ok || busy(w, dv, j.Name) {
		return
	}
	suffix := res.suffix
	req := &daemon.RunRequest{
		RunID: admin.NewID(now), Op: admin.OpRestore, Job: j.Name, Snapshot: res.snap.ID, Target: res.target.Name,
		Overrides: maskOverrides(res.overrides), RestoreSuffix: &suffix, RequestedBy: &actor,
	}
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionRestore, Result: ResultAccepted, Job: j.Name, Snapshot: res.snap.ID,
		Target: res.target.Name, RunID: req.RunID, Reason: in.Reason, Params: paramsWithSuffix(nil, suffix),
		Summary: displayName(actor) + " started a restore of " + j.Name + " into " + res.target.Name}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.dispatch(ctx, req); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionRestore, Result: ResultFailed, Job: j.Name, Snapshot: res.snap.ID, Target: res.target.Name,
			RunID: req.RunID, Error: dispatchMessage(err), Summary: "The restore of " + j.Name + " requested by " + displayName(actor) + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"needs_approval": false, "run_id": req.RunID})
}

func paramsWithSuffix(p map[string]string, suffix string) map[string]string {
	if suffix == "" {
		return p
	}
	if p == nil {
		p = map[string]string{}
	}
	p["restore_suffix"] = suffix
	return p
}

func (s *Server) activeRuns(w http.ResponseWriter, rq *request) {
	active := rq.URL.Query().Get("active") != "false"
	runs := []admin.Run{}
	for _, r := range s.liveRuns(rq.Context(), active) {
		if r.Trigger == admin.TriggerUI {
			runs = append(runs, r)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) getRun(w http.ResponseWriter, rq *request) {
	id := rq.PathValue("run")
	if !admin.ValidID(id) {
		writeError(w, http.StatusNotFound, codeNotFound, "run not found", nil)
		return
	}
	ctx := rq.Context()
	if s.client != nil {
		run, err := s.client.Run(ctx, id)
		if err == nil {
			writeJSON(w, http.StatusOK, run)
			return
		}
	}
	tr, tracked := s.tracker.get(id)
	if tracked {
		if j, err := s.cfg.Job(tr.Job); err == nil {
			s.data.invalidate(j.Name)
			for _, r := range s.data.jobSync(ctx, j).runs {
				if r.ID == id {
					writeJSON(w, http.StatusOK, r.Run())
					return
				}
			}
		}
	}
	for _, d := range s.data.all(ctx) {
		for _, r := range d.runs {
			if r.ID == id {
				writeJSON(w, http.StatusOK, r.Run())
				return
			}
		}
	}
	if tracked {
		writeJSON(w, http.StatusOK, admin.Run{
			ID: id, Job: tr.Job, Op: tr.Op, State: admin.StateUnknown, Trigger: admin.TriggerUI, RequestedBy: tr.RequestedBy,
			ApprovalID: tr.ApprovalID, Snapshot: tr.Snapshot, Target: tr.Target, QueuedAt: tptr(tr.queued),
		})
		return
	}
	writeError(w, http.StatusNotFound, codeNotFound, "run not found", nil)
}

type trackedRun struct {
	daemon.RunRequest
	queued time.Time
	done   bool
}

type runTracker struct {
	s  *Server
	mu sync.Mutex
	m  map[string]*trackedRun
}

func newRunTracker(s *Server) *runTracker { return &runTracker{s: s, m: map[string]*trackedRun{}} }

func (t *runTracker) add(req *daemon.RunRequest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.s.now().UTC()
	for id, tr := range t.m {
		if now.Sub(tr.queued) > 48*time.Hour {
			delete(t.m, id)
		}
	}
	t.m[req.RunID] = &trackedRun{RunRequest: *req, queued: now}
}

func (t *runTracker) get(id string) (trackedRun, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.m[id]
	if !ok {
		return trackedRun{}, false
	}
	return *tr, true
}

func (t *runTracker) loop(ctx context.Context) {
	if t.s.client == nil {
		return
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.poll(ctx)
		}
	}
}

func (t *runTracker) poll(ctx context.Context) {
	t.mu.Lock()
	var pending []trackedRun
	for _, tr := range t.m {
		if !tr.done {
			pending = append(pending, *tr)
		}
	}
	t.mu.Unlock()
	for _, tr := range pending {
		run, err := t.s.client.Run(ctx, tr.RunID)
		var result, errText string
		switch {
		case errors.Is(err, daemon.ErrNotFound):
			result, errText = ResultFailed, "the daemon no longer knows this run (it may have restarted)"
		case err != nil:
			continue
		case run.State == admin.StateSucceeded:
			result = ResultSucceeded
		case run.State == admin.StateFailed:
			result, errText = ResultFailed, run.Error
		default:
			continue
		}
		snap := tr.Snapshot
		if run != nil && run.Snapshot != "" {
			snap = run.Snapshot
		}
		actor := actorSystem
		if tr.RequestedBy != nil {
			actor = *tr.RequestedBy
		}
		e := &AuditEntry{
			Actor: actor, Action: ActionRunFinish, Result: result, Job: tr.Job, Snapshot: snap, Target: tr.Target,
			RunID: tr.RunID, ApprovalID: tr.ApprovalID, Error: safeText(errText, 1000), Params: map[string]string{"op": tr.Op},
			Summary: "The " + tr.Op + " of " + tr.Job + " requested by " + displayName(actor) + " " + result,
		}
		if err := t.s.audit.write(ctx, e); err != nil {
			t.s.log.Error("writing audit entry failed", "action", e.Action, "err", err)
			continue
		}
		t.s.data.invalidate(tr.Job)
		t.mu.Lock()
		if cur := t.m[tr.RunID]; cur != nil {
			cur.done = true
		}
		t.mu.Unlock()
	}
}
