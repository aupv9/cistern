package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Daemon struct {
	APIToken  string    `yaml:"api_token"`
	APIListen string    `yaml:"api_listen"`
	APITLS    DaemonTLS `yaml:"api_tls"`
}

type DaemonTLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

const DefaultDaemonAPIListen = ":9465"

func (d *Daemon) normalize(c *Config) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if d.APIToken != "" && len(d.APIToken) < MinSecretLength {
		add("daemon.api_token: must be at least %d characters", MinSecretLength)
	}
	if d.APIListen == "" && d.APIToken != "" {
		d.APIListen = DefaultDaemonAPIListen
	}
	if d.APIListen != "" {
		if _, _, err := net.SplitHostPort(d.APIListen); err != nil {
			add("daemon.api_listen: want host:port, got %q", d.APIListen)
		} else if d.APIListen == c.Metrics.Listen {
			add("daemon.api_listen: must differ from metrics.listen (%s); the internal API has its own listener", c.Metrics.Listen)
		}
	}
	if (d.APITLS.CertFile == "") != (d.APITLS.KeyFile == "") {
		add("daemon.api_tls: cert_file and key_file must be set together")
	}
	return errs
}

type Server struct {
	Name               string         `yaml:"name"`
	Listen             string         `yaml:"listen"`
	PublicURL          string         `yaml:"public_url"`
	TLS                ServerTLS      `yaml:"tls"`
	SessionKey         string         `yaml:"session_key"`
	SessionTTL         time.Duration  `yaml:"session_ttl"`
	IdleTimeout        time.Duration  `yaml:"idle_timeout"`
	StateRepository    string         `yaml:"state_repository"`
	AuditRetentionDays int            `yaml:"audit_retention_days"`
	OIDC               OIDC           `yaml:"oidc"`
	Roles              []*RoleMapping `yaml:"roles"`
	Daemon             DaemonClient   `yaml:"daemon"`
	RateLimit          RateLimit      `yaml:"rate_limit"`
	TrustedProxies     []string       `yaml:"trusted_proxies"`

	TrustedPrefixes []netip.Prefix `yaml:"-"`
}

type RateLimit struct {
	LoginPerMinute *int `yaml:"login_per_minute"`
	WritePerMinute *int `yaml:"write_per_minute"`
	ReadPerMinute  *int `yaml:"read_per_minute"`
}

const (
	DefaultLoginPerMinute = 10
	DefaultWritePerMinute = 30
	DefaultReadPerMinute  = 600
)

type ServerTLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Upstream bool   `yaml:"upstream"`
}

type OIDC struct {
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Scopes       []string `yaml:"scopes"`
	GroupsClaim  string   `yaml:"groups_claim"`
	EmailClaim   string   `yaml:"email_claim"`
	NameClaim    string   `yaml:"name_claim"`
}

type RoleMapping struct {
	Group string   `yaml:"group"`
	Roles []string `yaml:"roles"`
}

type DaemonClient struct {
	URL    string `yaml:"url"`
	Token  string `yaml:"token"`
	CAFile string `yaml:"ca_file"`
}

const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleRestorer = "restorer"
	RoleApprover = "approver"
	RoleAdmin    = "admin"
)

var validRoles = map[string]bool{RoleViewer: true, RoleOperator: true, RoleRestorer: true, RoleApprover: true, RoleAdmin: true}

const (
	DefaultServerListen       = "127.0.0.1:8443"
	MaxSessionTTL             = 8 * time.Hour
	MinAuditRetention         = 365
	DefaultAuditRetentionDays = 400
	MinSecretLength           = 32
)

func (s *Server) normalize(c *Config) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if s.Name == "" {
		s.Name = "cistern"
	}
	if s.Listen == "" {
		s.Listen = DefaultServerListen
	}
	if _, _, err := net.SplitHostPort(s.Listen); err != nil {
		add("server.listen: want host:port, got %q", s.Listen)
	}
	if s.PublicURL != "" {
		u, err := url.Parse(s.PublicURL)
		switch {
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			add("server.public_url: want http(s)://host[:port], got %q", s.PublicURL)
		case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
			add("server.public_url: must not have a path, query or fragment (the UI is served at /)")
		default:
			s.PublicURL = strings.TrimSuffix(s.PublicURL, "/")
		}
	}
	if (s.TLS.CertFile == "") != (s.TLS.KeyFile == "") {
		add("server.tls: cert_file and key_file must be set together")
	}
	if s.TLS.CertFile != "" && s.TLS.Upstream {
		add("server.tls: set either cert_file/key_file or upstream: true, not both")
	}
	if s.SessionKey != "" && len(s.SessionKey) < MinSecretLength {
		add("server.session_key: must be at least %d bytes", MinSecretLength)
	}
	if s.SessionTTL == 0 {
		s.SessionTTL = MaxSessionTTL
	}
	if s.SessionTTL < time.Minute || s.SessionTTL > MaxSessionTTL {
		add("server.session_ttl: must be between 1m and 8h")
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = 30 * time.Minute
	}
	if s.IdleTimeout < time.Minute || s.IdleTimeout > s.SessionTTL {
		add("server.idle_timeout: must be between 1m and session_ttl")
	}
	if s.StateRepository == "" {
		s.StateRepository = c.Defaults.Repository
	}
	if s.StateRepository == "" {
		add("server.state_repository: required (or set defaults.repository)")
	} else if c.Repositories[s.StateRepository] == nil {
		add("server.state_repository: unknown repository %q", s.StateRepository)
	}
	if s.AuditRetentionDays == 0 {
		s.AuditRetentionDays = DefaultAuditRetentionDays
	}
	if s.AuditRetentionDays < MinAuditRetention {
		add("server.audit_retention_days: must be at least %d", MinAuditRetention)
	}
	if s.OIDC.GroupsClaim == "" {
		s.OIDC.GroupsClaim = "groups"
	}
	if s.OIDC.EmailClaim == "" {
		s.OIDC.EmailClaim = "email"
	}
	if s.OIDC.NameClaim == "" {
		s.OIDC.NameClaim = "name"
	}
	if s.OIDC.Issuer != "" {
		if u, err := url.Parse(s.OIDC.Issuer); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("server.oidc.issuer: want an http(s) URL, got %q", s.OIDC.Issuer)
		}
	}
	for i, r := range s.Roles {
		if r == nil || r.Group == "" {
			add("server.roles[%d].group: required", i)
			continue
		}
		if len(r.Roles) == 0 {
			add("server.roles[%d].roles: at least one role is required", i)
		}
		for _, name := range r.Roles {
			if !validRoles[name] {
				add("server.roles[%d].roles: unknown role %q (viewer, operator, restorer, approver, admin)", i, name)
			}
		}
	}
	if s.Daemon.URL != "" {
		if u, err := url.Parse(s.Daemon.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("server.daemon.url: want http(s)://host:port, got %q", s.Daemon.URL)
		}
		if s.Daemon.Token != "" && len(s.Daemon.Token) < MinSecretLength {
			add("server.daemon.token: must be at least %d characters", MinSecretLength)
		}
	}
	if s.Daemon.CAFile != "" && !strings.HasPrefix(s.Daemon.URL, "https://") {
		add("server.daemon.ca_file: only used with an https daemon.url")
	}
	for _, f := range []struct {
		name string
		v    **int
		def  int
	}{
		{"login_per_minute", &s.RateLimit.LoginPerMinute, DefaultLoginPerMinute},
		{"write_per_minute", &s.RateLimit.WritePerMinute, DefaultWritePerMinute},
		{"read_per_minute", &s.RateLimit.ReadPerMinute, DefaultReadPerMinute},
	} {
		if *f.v == nil {
			d := f.def
			*f.v = &d
		}
		if n := **f.v; n < 0 || n > 100000 {
			add("server.rate_limit.%s: must be between 0 (off) and 100000, got %d", f.name, n)
		}
	}
	s.TrustedPrefixes = nil
	for i, p := range s.TrustedProxies {
		pfx, err := parsePrefix(p)
		if err != nil {
			add("server.trusted_proxies[%d]: want a CIDR or an IP address, got %q", i, p)
			continue
		}
		s.TrustedPrefixes = append(s.TrustedPrefixes, pfx)
	}
	return errs
}

func parsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (r RateLimit) Login() int { return derefInt(r.LoginPerMinute, DefaultLoginPerMinute) }
func (r RateLimit) Write() int { return derefInt(r.WritePerMinute, DefaultWritePerMinute) }
func (r RateLimit) Read() int  { return derefInt(r.ReadPerMinute, DefaultReadPerMinute) }

func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func IsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) ValidateFor(dev bool) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if s.Daemon.URL != "" && s.Daemon.Token == "" {
		add("server.daemon.token: required with daemon.url")
	}
	if dev {
		if !IsLoopback(s.Listen) {
			add("--insecure-dev-login: the listen address must be a loopback address, got %q", s.Listen)
		}
		if s.TLS.CertFile != "" || s.TLS.Upstream {
			add("--insecure-dev-login: only allowed without server.tls")
		}
		return errors.Join(errs...)
	}
	if s.TLS.CertFile == "" && !s.TLS.Upstream {
		add("server.tls: set cert_file and key_file, or upstream: true when a proxy terminates TLS")
	}
	if s.PublicURL == "" {
		add("server.public_url: required")
	} else if !strings.HasPrefix(s.PublicURL, "https://") {
		add("server.public_url: must be https")
	}
	if s.SessionKey == "" {
		add("server.session_key: required (at least %d bytes, e.g. from an environment variable)", MinSecretLength)
	}
	if s.OIDC.Issuer == "" || s.OIDC.ClientID == "" {
		add("server.oidc: issuer and client_id are required")
	}
	if len(s.Roles) == 0 {
		add("server.roles: map at least one group to a role")
	}
	return errors.Join(errs...)
}

var (
	ErrUnknownTarget     = errors.New("unknown restore target")
	ErrSuffixUnsupported = errors.New("restore_suffix is not supported by this driver")
	ErrInvalidSuffix     = errors.New("restore_suffix must match ^_[a-z0-9_]{1,30}$")
)

const DefaultRestoreSuffix = "_restored"

var suffixRe = regexp.MustCompile(`^_[a-z0-9_]{1,30}$`)

func SupportsRestoreSuffix(driver string) bool { return driver == "postgres" || driver == "mysql" }

func (j *Job) RestoreOverrides(target string, suffix *string) (map[string]string, *RestoreTarget, error) {
	t, ok := j.Target(target)
	if !ok {
		return nil, nil, fmt.Errorf("%w %q for job %s", ErrUnknownTarget, target, j.Name)
	}
	out := map[string]string{}
	for k, v := range t.Set {
		out[k] = v
	}
	if !SupportsRestoreSuffix(j.Driver) {
		if suffix != nil && *suffix != "" {
			return nil, t, ErrSuffixUnsupported
		}
		return out, t, nil
	}
	switch {
	case suffix == nil:
		if _, set := out["restore_suffix"]; !set {
			out["restore_suffix"] = DefaultRestoreSuffix
		}
	case *suffix == "":
		delete(out, "restore_suffix")
	case !suffixRe.MatchString(*suffix):
		return nil, t, ErrInvalidSuffix
	default:
		out["restore_suffix"] = *suffix
	}
	return out, t, nil
}
