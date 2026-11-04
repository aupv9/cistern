package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cistern/internal/admin"
	"cistern/internal/config"
	"cistern/internal/server/oidc"
)

func safeReturnTo(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") ||
		strings.ContainsAny(s, "\r\n\t\\") || strings.HasPrefix(s, "/api/") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return s
}

func (s *Server) deniedRedirect(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/denied?reason="+url.QueryEscape(reason), http.StatusFound)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		s.deniedRedirect(w, r, "sso_not_configured")
		return
	}
	ar, err := oidc.NewAuthRequest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	target, err := s.oidc.AuthURL(r.Context(), ar)
	if err != nil {
		s.log.Warn("starting sign-in failed", "err", err)
		s.deniedRedirect(w, r, "idp_unavailable")
		return
	}
	st := loginState{Request: ar, ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")), Expires: s.now().Add(10 * time.Minute).Unix()}
	val, err := s.logins.seal(st, s.cookies.login)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	s.cookies.set(w, s.cookies.login, val, 10*time.Minute, http.SameSiteLaxMode)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := r.Cookie(s.cookies.login)
	s.cookies.set(w, s.cookies.login, "", -1, http.SameSiteLaxMode)
	if s.oidc == nil || err != nil {
		s.deniedRedirect(w, r, "login_failed")
		return
	}
	var st loginState
	if s.logins.open(c.Value, s.cookies.login, &st) != nil || s.now().Unix() > st.Expires {
		s.deniedRedirect(w, r, "login_expired")
		return
	}
	q := r.URL.Query()
	if !equalSecret(q.Get("state"), st.Request.State) {
		s.deniedRedirect(w, r, "login_failed")
		return
	}
	if e := q.Get("error"); e != "" {
		s.log.Warn("identity provider returned an error", "error", safeText(e, 64))
		s.deniedRedirect(w, r, "login_failed")
		return
	}
	id, err := s.oidc.Exchange(r.Context(), q.Get("code"), st.Request)
	if err != nil {
		s.log.Warn("sign-in failed", "err", err)
		reason := "login_failed"
		if errors.Is(err, oidc.ErrUnavailable) {
			reason = "idp_unavailable"
		}
		s.deniedRedirect(w, r, reason)
		return
	}
	actor := admin.Actor{ID: id.Subject, Email: id.Email, Name: id.Name}
	roles := s.mapRoles(id.Groups)
	if len(roles) == 0 {
		s.audit.write(r.Context(), &AuditEntry{
			Actor: actor, Action: ActionAuthDenied, Result: ResultDenied, SourceIP: s.clientIP(r),
			Error: "no group maps to a role", Summary: displayName(actor) + " signed in but has no Cistern role",
		})
		s.deniedRedirect(w, r, "no_role")
		return
	}
	sess := s.newSession(actor, roles)
	if err := s.issueSession(w, sess); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	s.audit.write(r.Context(), &AuditEntry{
		Actor: actor, Roles: roles, Action: ActionLogin, Result: ResultSucceeded, SourceIP: s.clientIP(r),
		Summary: displayName(actor) + " signed in as " + strings.Join(roles, ", "),
	})
	http.Redirect(w, r, st.ReturnTo, http.StatusFound)
}

func safeText(s string, n int) string {
	var b strings.Builder
	for _, c := range s {
		if b.Len() >= n {
			break
		}
		if c >= 0x20 && c < 0x7f {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (s *Server) newSession(actor admin.Actor, roles []string) *session {
	now := s.now()
	return &session{
		ID: randomToken(16), Subject: actor.ID, Email: actor.Email, Name: actor.Name, Roles: roles,
		CSRF: randomToken(32), Issued: now.Unix(), Expires: now.Add(s.scfg.SessionTTL).Unix(), Last: now.Unix(),
	}
}

func (s *Server) logout(w http.ResponseWriter, rq *request) {
	e := &AuditEntry{Action: ActionLogout, Result: ResultSucceeded, Summary: displayName(rq.actor()) + " signed out"}
	if err := s.revoked.revoke(rq.Context(), rq.sess); err != nil {
		s.log.Error("storing the session revocation failed: the session is revoked on this server only", "err", err)
		e.Error = "the revocation could not be stored; other server replicas accept the session until it expires"
	}
	s.cookies.set(w, s.cookies.session, "", -1, http.SameSiteStrictMode)
	s.writeAudit(rq.Context(), rq, e)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	if !s.dev {
		writeError(w, http.StatusNotFound, codeNotFound, "no such API endpoint", nil)
		return
	}
	q := r.URL.Query()
	email := q.Get("email")
	if email == "" {
		email = "dev@localhost"
	}
	name := q.Get("name")
	if name == "" {
		name = email
	}
	sub := q.Get("sub")
	if sub == "" {
		sub = "dev:" + email
	}
	var roles []string
	rs := q.Get("roles")
	if rs == "" {
		rs = config.RoleAdmin
	}
	for _, role := range strings.Split(rs, ",") {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if !hasRole(roleOrder, role) {
			invalid(w, map[string]string{"roles": "unknown role " + role})
			return
		}
		roles = append(roles, role)
	}
	roles = expandRoles(roles)
	actor := admin.Actor{ID: sub, Email: email, Name: name}
	sess := s.newSession(actor, roles)
	if err := s.issueSession(w, sess); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
		return
	}
	s.audit.write(r.Context(), &AuditEntry{
		Actor: actor, Roles: roles, Action: ActionLogin, Result: ResultSucceeded, SourceIP: s.clientIP(r),
		Params: map[string]string{"method": "dev-login"}, Summary: displayName(actor) + " signed in with the insecure dev login",
	})
	if rt := q.Get("return_to"); rt != "" {
		http.Redirect(w, r, safeReturnTo(rt), http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, s.meView(r.Context(), sess))
}

type meUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type deploymentDaemon struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Version    string `json:"version,omitempty"`
}

type deploymentFeatures struct {
	Prune  bool `json:"prune"`
	Drills bool `json:"drills"`
}

type deployment struct {
	Name     string             `json:"name"`
	Version  string             `json:"version"`
	Timezone string             `json:"timezone"`
	Daemon   deploymentDaemon   `json:"daemon"`
	Features deploymentFeatures `json:"features"`
}

type meResponse struct {
	User             meUser     `json:"user"`
	Roles            []string   `json:"roles"`
	CSRFToken        string     `json:"csrf_token"`
	SessionExpiresAt time.Time  `json:"session_expires_at"`
	Deployment       deployment `json:"deployment"`
}

func (s *Server) deployment(ctx context.Context) deployment {
	dv := s.daemon.get(ctx, false)
	d := deployment{Name: s.scfg.Name, Version: s.version, Timezone: s.timezone(dv), Daemon: deploymentDaemon{Configured: dv.configured, Reachable: dv.reachable}}
	if dv.status != nil {
		d.Daemon.Version = dv.status.Version
	}
	d.Features.Prune = s.scfg.Prune.Enabled
	for _, j := range s.cfg.Jobs {
		d.Features.Drills = d.Features.Drills || j.Drill != nil
	}
	return d
}

func (s *Server) timezone(dv daemonView) string {
	if tz := s.cfg.Scheduler.Timezone; tz != "" {
		return tz
	}
	if dv.status != nil && dv.status.Timezone != "" && dv.status.Timezone != "Local" {
		return dv.status.Timezone
	}
	if tz := time.Local.String(); tz != "Local" {
		return tz
	}
	return "UTC"
}

func (s *Server) meView(ctx context.Context, sess *session) meResponse {
	return meResponse{
		User:  meUser{ID: sess.Subject, Email: sess.Email, Name: sess.Name},
		Roles: sess.Roles, CSRFToken: sess.CSRF, SessionExpiresAt: sess.expiresAt(), Deployment: s.deployment(ctx),
	}
}

func (s *Server) me(w http.ResponseWriter, rq *request) {
	if len(rq.sess.Roles) == 0 {
		writeError(w, http.StatusForbidden, codeForbidden, "you are signed in, but none of your groups maps to a Cistern role", nil)
		return
	}
	writeJSON(w, http.StatusOK, s.meView(rq.Context(), rq.sess))
}
