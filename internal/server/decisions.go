package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cistern/internal/admin"
	"cistern/internal/daemon"
)

type approvalView struct {
	ID            string       `json:"id"`
	State         string       `json:"state"`
	Job           string       `json:"job"`
	Snapshot      string       `json:"snapshot"`
	Target        targetView   `json:"target"`
	RestoreSuffix string       `json:"restore_suffix"`
	Reason        string       `json:"reason"`
	RequestedBy   admin.Actor  `json:"requested_by"`
	RequestedAt   time.Time    `json:"requested_at"`
	ExpiresAt     time.Time    `json:"expires_at"`
	DecidedBy     *admin.Actor `json:"decided_by,omitempty"`
	DecidedAt     *time.Time   `json:"decided_at,omitempty"`
	Note          string       `json:"note,omitempty"`
	RunID         string       `json:"run_id,omitempty"`
	CanDecide     bool         `json:"can_decide"`
}

func (s *Server) approvalView(a *approvalRecord, sess *session, now time.Time) approvalView {
	req := a.Request
	v := approvalView{
		ID: req.ID, State: a.state(now), Job: req.Job, Snapshot: req.Snapshot, Reason: req.Reason,
		RequestedBy: req.RequestedBy, RequestedAt: req.RequestedAt.UTC(), ExpiresAt: a.expiresAt().UTC(),
		Target: targetView{Name: req.Target, Production: true},
	}
	if req.RestoreSuffix != nil {
		v.RestoreSuffix = *req.RestoreSuffix
	}
	if j, err := s.cfg.Job(req.Job); err == nil {
		if t, ok := j.Target(req.Target); ok {
			v.Target = targetView{Name: t.Name, Description: t.Description, Production: t.IsProduction()}
		}
	}
	if d := a.Decision; d != nil {
		v.DecidedBy, v.DecidedAt, v.Note = d.DecidedBy, tptr(d.DecidedAt), d.Note
	}
	if a.Run != nil {
		v.RunID = a.Run.RunID
	}
	v.CanDecide = v.State == ApprovalPending && canDecide(sess.Roles) && sess.Subject != req.RequestedBy.ID
	return v
}

func (s *Server) expire(ctx context.Context, a *approvalRecord) {
	d := &approvalDecision{State: ApprovalExpired, DecidedAt: a.expiresAt().UTC()}
	if err := s.approvals.decide(ctx, a.Request.ID, d); err != nil {
		if !errors.Is(err, errDecided) {
			s.log.Warn("recording approval expiry failed", "approval", a.Request.ID, "err", err)
		}
		return
	}
	a.Decision = d
	if err := s.audit.write(ctx, &AuditEntry{
		Actor: actorSystem, Action: ActionApprovalExpired, Result: ResultDenied, Job: a.Request.Job, Snapshot: a.Request.Snapshot,
		Target: a.Request.Target, ApprovalID: a.Request.ID,
		Summary: "The restore request of " + displayName(a.Request.RequestedBy) + " for " + a.Request.Job + " expired without a decision",
	}); err != nil {
		s.log.Error("writing audit entry failed", "action", ActionApprovalExpired, "err", err)
	}
}

func (s *Server) listApprovals(w http.ResponseWriter, rq *request) {
	state := rq.URL.Query().Get("state")
	switch state {
	case "":
		state = "all"
	case "pending", "decided", "all":
	default:
		invalid(w, map[string]string{"state": "must be pending, decided or all"})
		return
	}
	limit, ok := intParam(rq, "limit", 50, 200)
	if !ok {
		invalid(w, map[string]string{"limit": "must be a positive integer"})
		return
	}
	ctx := rq.Context()
	list, err := s.approvals.list(ctx, false)
	if err != nil {
		s.log.Error("listing approvals failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the approval requests could not be read", nil)
		return
	}
	now := s.now().UTC()
	out := []approvalView{}
	for _, a := range list {
		if a.Decision == nil && a.state(now) == ApprovalExpired {
			s.expire(ctx, a)
		}
		v := s.approvalView(a, rq.sess, now)
		if (state == "pending" && v.State != ApprovalPending) || (state == "decided" && v.State == ApprovalPending) {
			continue
		}
		out = append(out, v)
		if len(out) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": out})
}

func (s *Server) approvalFor(w http.ResponseWriter, rq *request) (*approvalRecord, bool) {
	a, err := s.approvals.get(rq.Context(), rq.PathValue("approval"))
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "approval request not found", nil)
		return nil, false
	case err != nil:
		s.log.Error("reading approval failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the approval request could not be read", nil)
		return nil, false
	}
	if a.Decision == nil && a.state(s.now()) == ApprovalExpired {
		s.expire(rq.Context(), a)
	}
	return a, true
}

func (s *Server) getApproval(w http.ResponseWriter, rq *request) {
	a, ok := s.approvalFor(w, rq)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.approvalView(a, rq.sess, s.now().UTC()))
}

func (s *Server) checkDecidable(w http.ResponseWriter, rq *request, a *approvalRecord) bool {
	if st := a.state(s.now()); st != ApprovalPending {
		writeError(w, http.StatusConflict, codeConflict, "the request is already "+st, nil)
		return false
	}
	if a.Request.RequestedBy.ID == rq.sess.Subject {
		s.deny(rq, "approvers cannot decide their own requests")
		writeError(w, http.StatusForbidden, codeForbidden, "you cannot approve or reject your own request", nil)
		return false
	}
	return true
}

type decisionInput struct {
	Note string `json:"note"`
}

func (s *Server) approve(w http.ResponseWriter, rq *request) {
	a, ok := s.approvalFor(w, rq)
	if !ok || !s.checkDecidable(w, rq, a) {
		return
	}
	var in decisionInput
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) > 500 {
		invalid(w, map[string]string{"note": "must be at most 500 characters"})
		return
	}
	ctx := rq.Context()
	req := a.Request
	j, err := s.cfg.Job(req.Job)
	if err != nil {
		writeError(w, http.StatusConflict, codeConflict, "the job no longer exists", nil)
		return
	}
	in2 := &restoreInput{Snapshot: req.Snapshot, Target: req.Target, RestoreSuffix: req.RestoreSuffix}
	res, fields := s.resolveRestore(ctx, j, in2)
	if fields != nil {
		msg := "the request can no longer be carried out"
		for k, v := range fields {
			msg += "; " + k + ": " + v
		}
		writeError(w, http.StatusConflict, codeConflict, msg, nil)
		return
	}
	if res.snap.ID != req.Snapshot {
		writeError(w, http.StatusConflict, codeConflict, "the snapshot no longer exists", nil)
		return
	}
	dv, ok := s.requireDaemon(w, ctx)
	if !ok || busy(w, dv, j.Name) {
		return
	}
	actor := rq.actor()
	now := s.now().UTC()
	runID := admin.NewID(now)
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionApprove, Result: ResultAccepted, Job: j.Name, Snapshot: req.Snapshot, Target: req.Target,
		ApprovalID: req.ID, RunID: runID, Reason: in.Note, Params: map[string]string{"requested_by": displayName(req.RequestedBy)},
		Summary: displayName(actor) + " approved the restore of " + j.Name + " requested by " + displayName(req.RequestedBy)}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.approvals.decide(ctx, req.ID, &approvalDecision{State: ApprovalApproved, DecidedBy: &actor, DecidedAt: now, Note: in.Note}); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionApprove, Result: ResultFailed, Job: j.Name, ApprovalID: req.ID, RunID: runID,
			Error: "the decision could not be stored", Summary: "The approval by " + displayName(actor) + " was not recorded"})
		if errors.Is(err, errDecided) {
			writeError(w, http.StatusConflict, codeConflict, "the request was decided by someone else", nil)
			return
		}
		s.log.Error("storing approval decision failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "the decision could not be stored", nil)
		return
	}
	requester := req.RequestedBy
	suffix := res.suffix
	dreq := &daemon.RunRequest{
		RunID: runID, Op: "restore", Job: j.Name, Snapshot: req.Snapshot, Target: req.Target,
		Overrides: maskOverrides(res.overrides), RestoreSuffix: &suffix, RequestedBy: &requester, ApprovalID: req.ID,
	}
	if err := s.dispatch(ctx, dreq); err != nil {
		s.approvals.setRun(ctx, req.ID, &approvalRun{Error: dispatchMessage(err)})
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionRestore, Result: ResultFailed, Job: j.Name, Snapshot: req.Snapshot, Target: req.Target,
			ApprovalID: req.ID, RunID: runID, Error: dispatchMessage(err), Summary: "The approved restore of " + j.Name + " could not be started"})
		writeDispatchError(w, err)
		return
	}
	if err := s.approvals.setRun(ctx, req.ID, &approvalRun{RunID: runID}); err != nil {
		s.log.Warn("storing the run of an approval failed", "approval", req.ID, "err", err)
	}
	s.writeAudit(ctx, nil, &AuditEntry{Actor: requester, Action: ActionRestore, Result: ResultAccepted, Job: j.Name, Snapshot: req.Snapshot,
		Target: req.Target, ApprovalID: req.ID, RunID: runID, Reason: req.Reason, SourceIP: s.clientIP(rq.Request),
		Params:  paramsWithSuffix(map[string]string{"approved_by": displayName(actor)}, suffix),
		Summary: "Restore of " + j.Name + " into " + req.Target + " started after approval by " + displayName(actor)})
	a, err = s.approvals.get(ctx, req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.approvalView(a, rq.sess, s.now().UTC()))
}

func (s *Server) reject(w http.ResponseWriter, rq *request) {
	a, ok := s.approvalFor(w, rq)
	if !ok || !s.checkDecidable(w, rq, a) {
		return
	}
	var in decisionInput
	if err := decodeBody(rq, &in); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid JSON body", nil)
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if n := utf8.RuneCountInString(in.Note); n < 3 || n > 500 {
		invalid(w, map[string]string{"note": "must be 3 to 500 characters"})
		return
	}
	ctx := rq.Context()
	req := a.Request
	actor := rq.actor()
	now := s.now().UTC()
	if s.writeAudit(ctx, rq, &AuditEntry{Action: ActionReject, Result: ResultDenied, Job: req.Job, Snapshot: req.Snapshot, Target: req.Target,
		ApprovalID: req.ID, Reason: in.Note, Params: map[string]string{"requested_by": displayName(req.RequestedBy)},
		Summary: displayName(actor) + " rejected the restore of " + req.Job + " requested by " + displayName(req.RequestedBy)}) != nil {
		auditUnavailable(w)
		return
	}
	if err := s.approvals.decide(ctx, req.ID, &approvalDecision{State: ApprovalRejected, DecidedBy: &actor, DecidedAt: now, Note: in.Note}); err != nil {
		s.writeAudit(ctx, rq, &AuditEntry{Action: ActionReject, Result: ResultFailed, Job: req.Job, ApprovalID: req.ID,
			Error: "the decision could not be stored", Summary: "The rejection by " + displayName(actor) + " was not recorded"})
		if errors.Is(err, errDecided) {
			writeError(w, http.StatusConflict, codeConflict, "the request was decided by someone else", nil)
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "the decision could not be stored", nil)
		return
	}
	a, err := s.approvals.get(ctx, req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.approvalView(a, rq.sess, s.now().UTC()))
}

func (s *Server) parseAuditQuery(w http.ResponseWriter, rq *request, paged bool) (auditQuery, bool) {
	qs := rq.URL.Query()
	now := s.now().UTC()
	q := auditQuery{From: now.Add(-7 * 24 * time.Hour), To: now, Action: qs.Get("action"), Job: qs.Get("job"), Result: qs.Get("result"), Q: qs.Get("q")}
	fields := map[string]string{}
	if v := qs.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fields["from"] = "must be an RFC 3339 time"
		}
		q.From = t.UTC()
	}
	if v := qs.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fields["to"] = "must be an RFC 3339 time"
		}
		q.To = t.UTC()
	}
	if q.To.Before(q.From) {
		fields["from"] = "must be before to"
	}
	if q.To.Sub(q.From) > 800*24*time.Hour {
		fields["from"] = "the range may span at most 800 days"
	}
	if q.Action != "" && !auditActions[q.Action] {
		fields["action"] = "unknown action"
	}
	if q.Result != "" && !auditResults[q.Result] {
		fields["result"] = "unknown result"
	}
	if len(q.Q) > 200 {
		fields["q"] = "too long"
	}
	if paged {
		limit, ok := intParam(rq, "limit", 50, 500)
		if !ok {
			fields["limit"] = "must be a positive integer"
		}
		q.Limit = limit
		q.Cursor = qs.Get("cursor")
		if _, ok := auditIDTime(q.Cursor); q.Cursor != "" && !ok {
			fields["cursor"] = "invalid cursor"
		}
	}
	if len(fields) > 0 {
		invalid(w, fields)
		return q, false
	}
	return q, true
}

func (s *Server) listAudit(w http.ResponseWriter, rq *request) {
	q, ok := s.parseAuditQuery(w, rq, true)
	if !ok {
		return
	}
	entries, next, total, err := s.audit.pageTotal(rq.Context(), q, q.Cursor == "")
	if err != nil {
		s.log.Error("reading the audit log failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, codeInternal, "the audit log could not be read", nil)
		return
	}
	resp := map[string]any{"entries": entries}
	if next != "" {
		resp["next_cursor"] = next
	}
	if total >= 0 {
		resp["total"] = total
	}
	writeJSON(w, http.StatusOK, resp)
}

const exportMax = 200000

func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (s *Server) exportAudit(w http.ResponseWriter, rq *request) {
	format := rq.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "jsonl" {
		invalid(w, map[string]string{"format": "must be csv or jsonl"})
		return
	}
	q, ok := s.parseAuditQuery(w, rq, false)
	if !ok {
		return
	}
	name := "cistern-audit-" + q.From.Format("20060102") + "-" + q.To.Format("20060102") + "." + format
	h := w.Header()
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	n := 0
	var err error
	if format == "jsonl" {
		h.Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		err = s.audit.query(rq.Context(), q, func(e *AuditEntry) bool {
			n++
			return enc.Encode(e) == nil && n < exportMax
		})
	} else {
		h.Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		cw.Write([]string{"id", "time", "actor_id", "actor_email", "actor_name", "roles", "action", "result", "job", "snapshot", "target", "run_id", "approval_id", "reason", "error", "source_ip", "summary", "params"})
		err = s.audit.query(rq.Context(), q, func(e *AuditEntry) bool {
			params := ""
			if len(e.Params) > 0 {
				b, _ := json.Marshal(e.Params)
				params = string(b)
			}
			row := []string{e.ID, e.Time.UTC().Format(time.RFC3339Nano), e.Actor.ID, e.Actor.Email, e.Actor.Name, strings.Join(e.Roles, " "),
				e.Action, e.Result, e.Job, e.Snapshot, e.Target, e.RunID, e.ApprovalID, e.Reason, e.Error, e.SourceIP, e.Summary, params}
			for i := range row {
				row[i] = csvSafe(row[i])
			}
			n++
			return cw.Write(row) == nil && n < exportMax
		})
		cw.Flush()
	}
	if err != nil {
		s.log.Error("exporting the audit log failed", "err", err)
	}
}
