package server

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/engine"
)

func (s *Server) startDrill(w http.ResponseWriter, rq *request) {
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
	if !config.SupportsDrill(j.Driver) {
		writeError(w, http.StatusUnprocessableEntity, codeInvalid, engine.ErrDrillUnsupported.Error(), nil)
		return
	}
	if j.Drill == nil {
		writeError(w, http.StatusUnprocessableEntity, codeInvalid, "the job has no drill section in its configuration", nil)
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
	req := &daemon.RunRequest{RunID: admin.NewID(s.now()), Op: admin.OpDrill, Job: j.Name, Snapshot: m.ID, Target: j.Drill.Target, RequestedBy: &actor}
	params := map[string]string{"restore_suffix": j.Drill.RestoreSuffix}
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionDrill, Result: ResultAccepted, Job: j.Name, Snapshot: m.ID, Target: j.Drill.Target, RunID: req.RunID, Params: params,
		Summary: displayName(actor) + " started a restore drill of " + j.Name + " snapshot " + m.ID + " into " + j.Drill.Target}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.dispatch(ctx, req); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionDrill, Result: ResultFailed, Job: j.Name, Snapshot: m.ID, Target: j.Drill.Target, RunID: req.RunID, Error: dispatchMessage(err),
			Summary: "The restore drill of " + j.Name + " requested by " + displayName(actor) + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": req.RunID})
}

func (s *Server) listDrills(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	limit, ok := intParam(rq, "limit", 20, 100)
	if !ok {
		invalid(w, map[string]string{"limit": "must be a positive integer"})
		return
	}
	ctx := rq.Context()
	d := s.data.job(ctx, j)
	if d.err != nil && len(d.runs) == 0 {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the repository could not be read", nil)
		return
	}
	seen := map[string]bool{}
	for _, r := range d.runs {
		seen[r.ID] = true
	}
	out := []*admin.DrillResult{}
	for _, r := range s.liveRuns(ctx, false) {
		if r.Job == j.Name && r.Op == admin.OpDrill && r.Drill != nil && !seen[r.ID] {
			out = append(out, r.Drill)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RunID > out[b].RunID })
	for i := len(d.runs) - 1; i >= 0 && len(out) < limit; i-- {
		if r := d.runs[i]; r.Op == admin.OpDrill {
			out = append(out, admin.DrillResultOf(r.Run(), r.Drill))
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"drills": out})
}

type pruneItemView struct {
	Snapshot    string     `json:"snapshot"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Action      string     `json:"action"`
	Reasons     []string   `json:"reasons"`
	StoredBytes int64      `json:"stored_bytes,omitempty"`
	LockUntil   *time.Time `json:"lock_until,omitempty"`
}

type prunePreview struct {
	Job               string          `json:"job"`
	Items             []pruneItemView `json:"items"`
	Kept              int             `json:"kept"`
	Removed           int             `json:"removed"`
	FreedBytes        int64           `json:"freed_bytes"`
	Locked            int             `json:"locked"`
	RunRecordsRemoved int             `json:"run_records_removed"`
}

func (s *Server) prunePreview(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	rep, err := s.planner.Prune(rq.Context(), j.Name, true)
	if err != nil {
		s.log.Warn("prune preview failed", "job", j.Name, "err", err)
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the repository could not be read", nil)
		return
	}
	now := s.now()
	lock := s.objectLock(j.Repository)
	p := prunePreview{Job: j.Name, Items: []pruneItemView{}, RunRecordsRemoved: rep.RunRecordsRemoved}
	for _, it := range rep.Items {
		v := pruneItemView{Snapshot: it.Snapshot, FinishedAt: tptr(it.FinishedAt), Action: it.Action, Reasons: it.Reasons, StoredBytes: it.StoredBytes}
		if v.Reasons == nil {
			v.Reasons = []string{}
		}
		if lock != nil && !it.FinishedAt.IsZero() {
			v.LockUntil = tptr(it.FinishedAt.Add(time.Duration(lock.Days) * 24 * time.Hour))
		}
		if it.Action == engine.ActionKeep {
			p.Kept++
		} else {
			p.Removed++
			p.FreedBytes += it.StoredBytes
			if v.LockUntil != nil && v.LockUntil.After(now) {
				p.Locked++
			}
		}
		p.Items = append(p.Items, v)
	}
	writeJSON(w, http.StatusOK, p)
}

const pruneDisabled = "pruning from the UI is turned off (server.prune.enabled: false): the daemon would need permission to delete snapshots from the repository. Run cistern prune where those credentials live, or set server.prune.enabled: true"

func (s *Server) startPrune(w http.ResponseWriter, rq *request) {
	j, ok := s.jobFor(w, rq)
	if !ok {
		return
	}
	if !s.scfg.Prune.Enabled {
		s.deny(rq, "server.prune.enabled is false")
		writeError(w, http.StatusForbidden, codeForbidden, pruneDisabled, nil)
		return
	}
	var in struct {
		Reason     string `json:"reason"`
		ConfirmJob string `json:"confirm_job"`
	}
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	fields := map[string]string{}
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
	ctx := rq.Context()
	dv, ok := s.requireDaemon(w, ctx)
	if !ok || busy(w, dv, j.Name) {
		return
	}
	actor := rq.actor()
	req := &daemon.RunRequest{RunID: admin.NewID(s.now()), Op: admin.OpPrune, Job: j.Name, RequestedBy: &actor}
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionPrune, Result: ResultAccepted, Job: j.Name, RunID: req.RunID, Reason: in.Reason,
		Summary: displayName(actor) + " started a prune of " + j.Name}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.dispatch(ctx, req); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionPrune, Result: ResultFailed, Job: j.Name, RunID: req.RunID, Reason: in.Reason, Error: dispatchMessage(err),
			Summary: "The prune of " + j.Name + " requested by " + displayName(actor) + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": req.RunID})
}

type configView struct {
	Path           string                 `json:"path"`
	LoadedAt       time.Time              `json:"loaded_at"`
	YAML           string                 `json:"yaml"`
	LiteralSecrets []config.LiteralSecret `json:"literal_secrets"`
}

func (s *Server) configView(w http.ResponseWriter, rq *request) {
	data, err := os.ReadFile(s.cfg.Path)
	if err != nil {
		s.log.Warn("reading the configuration file failed", "path", s.cfg.Path, "err", err)
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the configuration file could not be read", nil)
		return
	}
	text, lits, err := config.RedactYAML(data)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the configuration file on disk could not be parsed; it may have been edited since the server started", nil)
		return
	}
	actor := rq.actor()
	if s.writeAudit(rq.Context(), rq, &AuditEntry{Action: ActionConfigView, Result: ResultSucceeded,
		Summary: displayName(actor) + " viewed the configuration"}) != nil {
		auditUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, configView{Path: s.cfg.Path, LoadedAt: s.loadedAt, YAML: text, LiteralSecrets: lits})
}
