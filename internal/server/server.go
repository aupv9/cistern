package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"cistern/internal/config"
	"cistern/internal/daemon"
	"cistern/internal/engine"
	"cistern/internal/server/oidc"
)

type Options struct {
	Config   *config.Config
	Version  string
	Listen   string
	DevLogin bool
	Log      *slog.Logger
	AuditOut io.Writer
	UI       fs.FS
}

type Server struct {
	cfg     *config.Config
	scfg    *config.Server
	version string
	dev     bool
	log     *slog.Logger
	now     func() time.Time

	sessions *sealer
	logins   *sealer
	cookies  cookieNames
	revoked  *revocations
	limits   limiters
	oidc     *oidc.Provider

	data      *store
	daemon    *daemonCache
	client    *daemon.Client
	audit     *auditStore
	approvals *approvalStore
	tracker   *runTracker
	planner   *engine.Engine
	loadedAt  time.Time

	handler http.Handler
}

func New(opt Options) (*Server, error) {
	cfg := opt.Config
	if cfg == nil || cfg.Server == nil {
		return nil, errors.New("config: the server section is missing")
	}
	sc := cfg.Server
	if opt.Listen != "" {
		sc.Listen = opt.Listen
	}
	if err := sc.ValidateFor(opt.DevLogin); err != nil {
		return nil, err
	}
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	if opt.AuditOut == nil {
		opt.AuditOut = os.Stdout
	}
	s := &Server{cfg: cfg, scfg: sc, version: opt.Version, dev: opt.DevLogin, log: log, now: time.Now, cookies: newCookieNames(opt.DevLogin)}
	s.limits = limiters{
		login: newLimiter(sc.RateLimit.Login(), 0),
		write: newLimiter(sc.RateLimit.Write(), 0),
		read:  newLimiter(sc.RateLimit.Read(), 0),
	}

	key := []byte(sc.SessionKey)
	if len(key) == 0 {
		key = []byte(randomToken(32))
		log.Warn("server.session_key is not set: using a random key, sessions end when the server restarts")
	}
	var err error
	if s.sessions, err = newSealer(key, "session"); err != nil {
		return nil, err
	}
	if s.logins, err = newSealer(key, "login"); err != nil {
		return nil, err
	}
	if sc.OIDC.Issuer != "" {
		publicURL := sc.PublicURL
		if publicURL == "" {
			publicURL = "http://" + sc.Listen
		}
		scopes := sc.OIDC.Scopes
		s.oidc = oidc.New(oidc.Config{
			Issuer: sc.OIDC.Issuer, ClientID: sc.OIDC.ClientID, ClientSecret: sc.OIDC.ClientSecret,
			RedirectURL: publicURL + "/api/v1/auth/callback", Scopes: scopes,
			GroupsClaim: sc.OIDC.GroupsClaim, EmailClaim: sc.OIDC.EmailClaim, NameClaim: sc.OIDC.NameClaim,
		})
	}

	s.loadedAt = time.Now().UTC()
	repos := engine.NewRepos(cfg, log)
	s.data = newStore(cfg, repos, log)
	s.planner = engine.NewPlanner(cfg, repos, log)
	stateRepo, statePrefix, err := repos.Open(sc.StateRepository)
	if err != nil {
		return nil, fmt.Errorf("server.state_repository: %w", err)
	}
	s.audit = newAuditStore(stateRepo, statePrefix, sc.AuditRetentionDays, opt.AuditOut)
	s.approvals = newApprovalStore(stateRepo, statePrefix, sc.AuditRetentionDays)
	s.revoked = newRevocations(stateRepo, statePrefix, log, func() time.Time { return s.now() })
	lctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = s.revoked.load(lctx)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("loading session revocations from server.state_repository: %w", err)
	}

	s.daemon = &daemonCache{now: time.Now}
	if sc.Daemon.URL != "" {
		hc, err := daemon.HTTPClient(cfg.Resolve(sc.Daemon.CAFile))
		if err != nil {
			return nil, fmt.Errorf("server.daemon.ca_file: %w", err)
		}
		if s.client, err = daemon.NewClient(sc.Daemon.URL, sc.Daemon.Token, hc); err != nil {
			return nil, err
		}
		s.daemon.client = s.client
	} else {
		log.Warn("server.daemon.url is not set: the UI is read-only")
	}
	s.tracker = newRunTracker(s)

	ui, err := newUIHandler(opt.UI, s.securityHeaders)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", ui)
	s.handler = s.recoverer(s.baseHeaders(mux))
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) tlsOn() bool { return s.scfg.TLS.CertFile != "" || s.scfg.TLS.Upstream }

func (s *Server) securityHeaders(h http.Header, csp string) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if s.tlsOn() {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func (s *Server) baseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.securityHeaders(w.Header(), apiCSP)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.log.Error("handler panicked", "path", r.URL.Path, "panic", p)
				writeError(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Run(ctx context.Context) error {
	if s.dev {
		s.log.Warn("!!! INSECURE DEV LOGIN ENABLED: anyone who can reach " + s.scfg.Listen + " can sign in with any role via /api/v1/auth/dev-login. Never use this outside local development. !!!")
	}
	ln, err := net.Listen("tcp", s.scfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10,
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	if s.scfg.TLS.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.Resolve(s.scfg.TLS.CertFile), s.cfg.Resolve(s.scfg.TLS.KeyFile))
		if err != nil {
			ln.Close()
			return fmt.Errorf("server.tls: %w", err)
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	var wg sync.WaitGroup
	tctx, stopTracker := context.WithCancel(ctx)
	wg.Add(2)
	go func() { defer wg.Done(); s.tracker.loop(tctx) }()
	go func() { defer wg.Done(); s.revoked.loop(tctx) }()

	errc := make(chan error, 1)
	useTLS := srv.TLSConfig != nil
	go func() {
		if useTLS {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	s.log.Info("admin server started", "listen", ln.Addr().String(), "scheme", scheme, "public_url", s.scfg.PublicURL,
		"daemon", s.scfg.Daemon.URL, "state_repository", s.scfg.StateRepository, "dev_login", s.dev)

	var runErr error
	select {
	case runErr = <-errc:
	case <-ctx.Done():
		s.log.Info("shutting down")
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := srv.Shutdown(sctx); err != nil && runErr == nil && !errors.Is(err, http.ErrServerClosed) {
		runErr = err
	}
	cancel()
	stopTracker()
	wg.Wait()
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}
