package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
)

const (
	codeUnauthenticated = "unauthenticated"
	codeForbidden       = "forbidden"
	codeNotFound        = "not_found"
	codeConflict        = "conflict"
	codeInvalid         = "invalid"
	codeDaemon          = "daemon_unavailable"
	codeCSRF            = "csrf"
	codeInternal        = "internal"
)

const csrfHeader = "X-Cistern-CSRF"

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, map[string]errorBody{"error": {Code: code, Message: msg, Fields: fields}})
}

func invalid(w http.ResponseWriter, fields map[string]string) {
	msg := "the request is invalid"
	if len(fields) == 1 {
		for k, v := range fields {
			msg = k + ": " + v
		}
	}
	writeError(w, http.StatusUnprocessableEntity, codeInvalid, msg, fields)
}

var roleOrder = []string{config.RoleViewer, config.RoleOperator, config.RoleRestorer, config.RoleApprover, config.RoleAdmin}

func expandRoles(roles []string) []string {
	set := map[string]bool{}
	for _, r := range roles {
		switch r {
		case config.RoleAdmin:
			set[config.RoleAdmin], set[config.RoleRestorer], set[config.RoleOperator], set[config.RoleViewer] = true, true, true, true
		case config.RoleRestorer:
			set[config.RoleRestorer], set[config.RoleOperator], set[config.RoleViewer] = true, true, true
		case config.RoleOperator:
			set[config.RoleOperator], set[config.RoleViewer] = true, true
		case config.RoleApprover:
			set[config.RoleApprover], set[config.RoleViewer] = true, true
		case config.RoleViewer:
			set[config.RoleViewer] = true
		}
	}
	out := []string{}
	for _, r := range roleOrder {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func canDecide(roles []string) bool {
	return hasRole(roles, config.RoleApprover) || hasRole(roles, config.RoleAdmin)
}

func (s *Server) mapRoles(groups []string) []string {
	in := map[string]bool{}
	for _, g := range groups {
		in[g] = true
	}
	var roles []string
	for _, m := range s.scfg.Roles {
		if in[m.Group] {
			roles = append(roles, m.Roles...)
		}
	}
	return expandRoles(roles)
}

type perm struct {
	name  string
	check func(roles []string) bool
}

var (
	permAny     = perm{"any", func([]string) bool { return true }}
	permView    = perm{config.RoleViewer, func(r []string) bool { return hasRole(r, config.RoleViewer) }}
	permOperate = perm{config.RoleOperator, func(r []string) bool { return hasRole(r, config.RoleOperator) }}
	permRestore = perm{config.RoleRestorer, func(r []string) bool { return hasRole(r, config.RoleRestorer) }}
	permDecide  = perm{config.RoleApprover, canDecide}
	permAdmin   = perm{config.RoleAdmin, func(r []string) bool { return hasRole(r, config.RoleAdmin) }}
)

type request struct {
	*http.Request
	sess *session
}

func (rq *request) actor() admin.Actor {
	return admin.Actor{ID: rq.sess.Subject, Email: rq.sess.Email, Name: rq.sess.Name}
}

type handlerFunc func(w http.ResponseWriter, rq *request)

func (s *Server) sessionFrom(r *http.Request) (*session, bool) {
	c, err := r.Cookie(s.cookies.session)
	if err != nil || c.Value == "" {
		return nil, false
	}
	var sess session
	if s.sessions.open(c.Value, s.cookies.session, &sess) != nil {
		return nil, false
	}
	now := s.now().Unix()
	if now >= sess.Expires || now-sess.Last > int64(s.scfg.IdleTimeout/time.Second) || s.revoked.has(sess.ID) {
		return nil, false
	}
	return &sess, true
}

func (s *Server) issueSession(w http.ResponseWriter, sess *session) error {
	val, err := s.sessions.seal(sess, s.cookies.session)
	if err != nil {
		return err
	}
	s.cookies.set(w, s.cookies.session, val, time.Until(sess.expiresAt()), http.SameSiteStrictMode)
	return nil
}

func (s *Server) api(p perm, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFrom(r)
		if !ok {
			if _, err := r.Cookie(s.cookies.session); err == nil {
				s.cookies.set(w, s.cookies.session, "", -1, http.SameSiteStrictMode)
			}
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "sign in required", nil)
			return
		}
		if !s.limitUser(w, r, sess) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !equalSecret(r.Header.Get(csrfHeader), sess.CSRF) {
				writeError(w, http.StatusForbidden, codeCSRF, "missing or wrong "+csrfHeader+" header", nil)
				return
			}
			if r.ContentLength != 0 {
				if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
					writeError(w, http.StatusUnsupportedMediaType, codeInvalid, "the request body must be application/json", nil)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		rq := &request{Request: r, sess: sess}
		if !p.check(sess.Roles) {
			s.deny(rq, "requires the "+p.name+" role")
			writeError(w, http.StatusForbidden, codeForbidden, "you do not have the "+p.name+" role", nil)
			return
		}
		now := s.now().Unix()
		if now-sess.Last >= 60 {
			sess.Last = now
			if err := s.issueSession(w, sess); err != nil {
				s.log.Warn("re-issuing the session cookie failed", "err", err)
			}
		}
		h(w, rq)
	}
}

func (s *Server) deny(rq *request, why string) {
	e := &AuditEntry{
		Actor: rq.actor(), Roles: rq.sess.Roles, Action: ActionAccessDenied, Result: ResultDenied,
		Job: rq.PathValue("job"), ApprovalID: rq.PathValue("approval"), SourceIP: s.clientIP(rq.Request),
		Params: map[string]string{"method": rq.Method, "path": rq.URL.Path}, Error: why,
		Summary: displayName(rq.actor()) + " was denied " + rq.Method + " " + rq.URL.Path,
	}
	if err := s.audit.write(rq.Context(), e); err != nil {
		s.log.Error("writing audit entry failed", "action", e.Action, "err", err)
	}
}

func displayName(a admin.Actor) string {
	if a.Email != "" {
		return a.Email
	}
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

func decodeBody(rq *request, v any) error {
	if rq.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(rq.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Server) clientIP(r *http.Request) string {
	return forwardedClient(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), s.scfg.TrustedPrefixes)
}

func (s *Server) writeAudit(ctx context.Context, rq *request, e *AuditEntry) error {
	if rq != nil {
		if e.Actor.ID == "" {
			e.Actor = rq.actor()
		}
		if e.Roles == nil {
			e.Roles = rq.sess.Roles
		}
		e.SourceIP = s.clientIP(rq.Request)
	}
	err := s.audit.write(ctx, e)
	if err != nil {
		s.log.Error("writing audit entry failed", "action", e.Action, "err", err)
	}
	return err
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/v1/auth/login", s.limitLogin(s.login))
	mux.HandleFunc("GET /api/v1/auth/callback", s.limitLogin(s.callback))
	mux.HandleFunc("POST /api/v1/auth/logout", s.api(permAny, s.logout))
	mux.HandleFunc("GET /api/v1/auth/dev-login", s.limitLogin(s.devLogin))
	mux.HandleFunc("GET /api/v1/me", s.api(permAny, s.me))

	mux.HandleFunc("GET /api/v1/overview", s.api(permView, s.overview))
	mux.HandleFunc("GET /api/v1/jobs/{job}", s.api(permView, s.jobDetail))
	mux.HandleFunc("GET /api/v1/jobs/{job}/snapshots", s.api(permView, s.snapshots))
	mux.HandleFunc("GET /api/v1/jobs/{job}/snapshots/{snapshot}", s.api(permView, s.snapshotManifest))
	mux.HandleFunc("GET /api/v1/jobs/{job}/snapshots/{snapshot}/manifest.json", s.api(permView, s.snapshotManifestFile))
	mux.HandleFunc("GET /api/v1/jobs/{job}/runs", s.api(permView, s.jobRuns))
	mux.HandleFunc("POST /api/v1/jobs/{job}/backup", s.api(permOperate, s.startBackup))
	mux.HandleFunc("POST /api/v1/jobs/{job}/verify", s.api(permOperate, s.startVerify))
	mux.HandleFunc("POST /api/v1/jobs/{job}/restore/preview", s.api(permRestore, s.restorePreview))
	mux.HandleFunc("POST /api/v1/jobs/{job}/restore", s.api(permRestore, s.startRestore))
	mux.HandleFunc("POST /api/v1/jobs/{job}/drill", s.api(permRestore, s.startDrill))
	mux.HandleFunc("GET /api/v1/jobs/{job}/drills", s.api(permView, s.listDrills))
	mux.HandleFunc("POST /api/v1/jobs/{job}/prune/preview", s.api(permOperate, s.prunePreview))
	mux.HandleFunc("POST /api/v1/jobs/{job}/prune", s.api(permAdmin, s.startPrune))
	mux.HandleFunc("GET /api/v1/config", s.api(permOperate, s.configView))
	mux.HandleFunc("GET /api/v1/runs", s.api(permView, s.activeRuns))
	mux.HandleFunc("GET /api/v1/runs/{run}", s.api(permView, s.getRun))
	mux.HandleFunc("GET /api/v1/approvals", s.api(permView, s.listApprovals))
	mux.HandleFunc("GET /api/v1/approvals/{approval}", s.api(permView, s.getApproval))
	mux.HandleFunc("POST /api/v1/approvals/{approval}/approve", s.api(permDecide, s.approve))
	mux.HandleFunc("POST /api/v1/approvals/{approval}/reject", s.api(permDecide, s.reject))
	mux.HandleFunc("GET /api/v1/audit", s.api(permView, s.listAudit))
	mux.HandleFunc("GET /api/v1/audit/export", s.api(permView, s.exportAudit))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such API endpoint", nil)
	})
}
