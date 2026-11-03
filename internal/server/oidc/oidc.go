package oidc

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("identity provider unavailable")

const (
	maxBody      = 1 << 20
	clockSkew    = 60 * time.Second
	refreshEvery = 30 * time.Second
	jwksMaxAge   = time.Hour
)

type Config struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
	Scopes                                      []string
	GroupsClaim                                 string
	EmailClaim                                  string
	NameClaim                                   string
	HTTPClient                                  *http.Client
	Now                                         func() time.Time
}

type AuthRequest struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

type Identity struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

type metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

type key struct {
	kid, kty, alg string
	rsa           *rsa.PublicKey
	ec            *ecdsa.PublicKey
}

type Provider struct {
	cfg    Config
	scopes []string
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	meta      *metadata
	keys      []key
	keysAt    time.Time
	fetchedAt time.Time

	fetchMu sync.Mutex
}

func New(cfg Config) *Provider {
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.EmailClaim == "" {
		cfg.EmailClaim = "email"
	}
	if cfg.NameClaim == "" {
		cfg.NameClaim = "name"
	}
	scopes := []string{"openid"}
	for _, s := range cfg.Scopes {
		if s != "" && s != "openid" {
			scopes = append(scopes, s)
		}
	}
	p := &Provider{cfg: cfg, scopes: scopes, client: cfg.HTTPClient, now: cfg.Now}
	if p.client == nil {
		p.client = &http.Client{Timeout: 10 * time.Second}
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p
}

func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func NewAuthRequest() (AuthRequest, error) {
	var ar AuthRequest
	var err error
	if ar.State, err = randomString(); err != nil {
		return ar, err
	}
	if ar.Nonce, err = randomString(); err != nil {
		return ar, err
	}
	ar.Verifier, err = randomString()
	return ar, err
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (p *Provider) getJSON(ctx context.Context, what, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, what, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, what, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("%w: %s: HTTP %d", ErrUnavailable, what, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return fmt.Errorf("%w: %s: invalid JSON: %v", ErrUnavailable, what, err)
	}
	return nil
}

func (p *Provider) checkEndpoint(name, s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: discovery: invalid %s", ErrUnavailable, name)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && strings.HasPrefix(p.cfg.Issuer, "http://") {
		return nil
	}
	return fmt.Errorf("%w: discovery: %s must use https", ErrUnavailable, name)
}

func (p *Provider) metadata(ctx context.Context) (*metadata, error) {
	p.mu.Lock()
	m := p.meta
	p.mu.Unlock()
	if m != nil {
		return m, nil
	}
	if p.cfg.Issuer == "" {
		return nil, fmt.Errorf("%w: no issuer configured", ErrUnavailable)
	}
	var d metadata
	if err := p.getJSON(ctx, "discovery", strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if d.Issuer != p.cfg.Issuer {
		return nil, fmt.Errorf("%w: discovery: issuer %q does not match the configured issuer %q", ErrUnavailable, d.Issuer, p.cfg.Issuer)
	}
	for _, e := range [][2]string{{"authorization_endpoint", d.AuthorizationEndpoint}, {"token_endpoint", d.TokenEndpoint}, {"jwks_uri", d.JWKSURI}} {
		if err := p.checkEndpoint(e[0], e[1]); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	if p.meta == nil {
		p.meta = &d
	}
	m = p.meta
	p.mu.Unlock()
	return m, nil
}

func (p *Provider) AuthURL(ctx context.Context, ar AuthRequest) (string, error) {
	m, err := p.metadata(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(m.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("%w: invalid authorization_endpoint", ErrUnavailable)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", p.cfg.RedirectURL)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", ar.State)
	q.Set("nonce", ar.Nonce)
	q.Set("code_challenge", challenge(ar.Verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func safeCode(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "error"
		}
	}
	if s == "" {
		return "error"
	}
	return s
}

func (p *Provider) Exchange(ctx context.Context, code string, ar AuthRequest) (*Identity, error) {
	if code == "" {
		return nil, errors.New("oidc: missing authorization code")
	}
	m, err := p.metadata(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {ar.Verifier},
	}
	basic := false
	if p.cfg.ClientSecret != "" {
		methods := m.TokenAuthMethods
		if len(methods) > 0 && !contains(methods, "client_secret_basic") && contains(methods, "client_secret_post") {
			form.Set("client_id", p.cfg.ClientID)
			form.Set("client_secret", p.cfg.ClientSecret)
		} else {
			basic = true
		}
	} else {
		form.Set("client_id", p.cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: token endpoint: invalid request", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token endpoint: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: token endpoint: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: token endpoint: HTTP %d", ErrUnavailable, resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &e)
		return nil, fmt.Errorf("oidc: token request rejected (HTTP %d): %s", resp.StatusCode, safeCode(e.Error))
	}
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, errors.New("oidc: token response is not valid JSON")
	}
	if tr.IDToken == "" {
		return nil, errors.New("oidc: token response has no id_token")
	}
	return p.VerifyIDToken(ctx, tr.IDToken, ar.Nonce)
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func parseKey(j jwk) (key, bool) {
	if j.Use != "" && j.Use != "sig" {
		return key{}, false
	}
	k := key{kid: j.Kid, kty: j.Kty, alg: j.Alg}
	switch j.Kty {
	case "RSA":
		n, err1 := base64.RawURLEncoding.DecodeString(j.N)
		e, err2 := base64.RawURLEncoding.DecodeString(j.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return key{}, false
		}
		nb := new(big.Int).SetBytes(n)
		eb := new(big.Int).SetBytes(e)
		if nb.BitLen() < 2048 || eb.Int64() < 3 || eb.Int64() > math.MaxInt32 {
			return key{}, false
		}
		k.rsa = &rsa.PublicKey{N: nb, E: int(eb.Int64())}
	case "EC":
		if j.Crv != "P-256" {
			return key{}, false
		}
		x, err1 := base64.RawURLEncoding.DecodeString(j.X)
		y, err2 := base64.RawURLEncoding.DecodeString(j.Y)
		if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
			return key{}, false
		}
		point := append(append([]byte{4}, x...), y...)
		if _, err := ecdh.P256().NewPublicKey(point); err != nil {
			return key{}, false
		}
		k.ec = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	default:
		return key{}, false
	}
	return k, true
}

func (p *Provider) refreshKeys(ctx context.Context) error {
	m, err := p.metadata(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.fetchedAt = p.now()
	p.mu.Unlock()
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := p.getJSON(ctx, "jwks", m.JWKSURI, &set); err != nil {
		return err
	}
	var keys []key
	for _, j := range set.Keys {
		if k, ok := parseKey(j); ok {
			keys = append(keys, k)
		}
	}
	p.mu.Lock()
	p.keys, p.keysAt = keys, p.now()
	p.mu.Unlock()
	return nil
}

func match(keys []key, kid, alg string) []key {
	var out []key
	for _, k := range keys {
		if kid != "" && k.kid != kid {
			continue
		}
		switch alg {
		case "RS256":
			if k.rsa == nil || (k.alg != "" && k.alg != "RS256") {
				continue
			}
		case "ES256":
			if k.ec == nil || (k.alg != "" && k.alg != "ES256") {
				continue
			}
		default:
			continue
		}
		out = append(out, k)
	}
	return out
}

var errUnknownKey = errors.New("oidc: id token: unknown signing key")

func (p *Provider) keysFor(ctx context.Context, kid, alg string) ([]key, error) {
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	p.mu.Lock()
	keys, at, fetched := p.keys, p.keysAt, p.fetchedAt
	p.mu.Unlock()
	now := p.now()
	if !at.IsZero() && now.Sub(at) <= jwksMaxAge {
		if c := match(keys, kid, alg); len(c) > 0 {
			return c, nil
		}
		if now.Sub(fetched) < refreshEvery {
			return nil, errUnknownKey
		}
	}
	if err := p.refreshKeys(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	keys = p.keys
	p.mu.Unlock()
	if c := match(keys, kid, alg); len(c) > 0 {
		return c, nil
	}
	return nil, errUnknownKey
}

func verifySig(k key, alg string, input, sig []byte) bool {
	h := sha256.Sum256(input)
	switch alg {
	case "RS256":
		return k.rsa != nil && rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, h[:], sig) == nil
	case "ES256":
		if k.ec == nil || len(sig) != 64 {
			return false
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(k.ec, h[:], r, s)
	}
	return false
}

func tokenErr(format string, a ...any) error {
	return fmt.Errorf("oidc: id token: "+format, a...)
}

func numericClaim(claims map[string]any, name string) (time.Time, error) {
	v, ok := claims[name]
	if !ok {
		return time.Time{}, tokenErr("missing %s", name)
	}
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, tokenErr("%s is not a number", name)
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return time.Time{}, tokenErr("%s is not a number", name)
	}
	return time.Unix(int64(f), 0), nil
}

func stringClaim(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}

func (p *Provider) VerifyIDToken(ctx context.Context, raw, nonce string) (*Identity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, tokenErr("malformed")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, tokenErr("malformed header")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, tokenErr("malformed header")
	}
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return nil, tokenErr("unsupported signing algorithm")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		return nil, tokenErr("malformed signature")
	}
	keys, err := p.keysFor(ctx, hdr.Kid, hdr.Alg)
	if err != nil {
		return nil, err
	}
	input := []byte(parts[0] + "." + parts[1])
	ok := false
	for _, k := range keys {
		if verifySig(k, hdr.Alg, input, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, tokenErr("invalid signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, tokenErr("malformed payload")
	}
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	var claims map[string]any
	if err := dec.Decode(&claims); err != nil || claims == nil {
		return nil, tokenErr("malformed payload")
	}
	if iss := stringClaim(claims, "iss"); iss != p.cfg.Issuer {
		return nil, tokenErr("issuer does not match")
	}
	var aud []string
	switch v := claims["aud"].(type) {
	case string:
		aud = []string{v}
	case []any:
		for _, a := range v {
			s, ok := a.(string)
			if !ok {
				return nil, tokenErr("invalid aud")
			}
			aud = append(aud, s)
		}
	default:
		return nil, tokenErr("missing aud")
	}
	if !contains(aud, p.cfg.ClientID) {
		return nil, tokenErr("audience does not include this client")
	}
	azp, hasAzp := claims["azp"]
	if len(aud) > 1 && !hasAzp {
		return nil, tokenErr("multiple audiences without azp")
	}
	if hasAzp {
		if s, _ := azp.(string); s != p.cfg.ClientID {
			return nil, tokenErr("azp does not match this client")
		}
	}
	now := p.now()
	exp, err := numericClaim(claims, "exp")
	if err != nil {
		return nil, err
	}
	if !now.Add(-clockSkew).Before(exp) {
		return nil, tokenErr("expired")
	}
	iat, err := numericClaim(claims, "iat")
	if err != nil {
		return nil, err
	}
	if iat.After(now.Add(clockSkew)) {
		return nil, tokenErr("issued in the future")
	}
	got := stringClaim(claims, "nonce")
	if nonce == "" || subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
		return nil, tokenErr("nonce does not match")
	}
	sub := stringClaim(claims, "sub")
	if sub == "" {
		return nil, tokenErr("missing sub")
	}
	id := &Identity{Subject: sub, Email: stringClaim(claims, p.cfg.EmailClaim), Name: stringClaim(claims, p.cfg.NameClaim)}
	switch g := claims[p.cfg.GroupsClaim].(type) {
	case string:
		if g != "" {
			id.Groups = []string{g}
		}
	case []any:
		for _, v := range g {
			if s, ok := v.(string); ok {
				id.Groups = append(id.Groups, s)
			}
		}
	}
	if id.Name == "" {
		id.Name = id.Email
	}
	if id.Name == "" {
		id.Name = id.Subject
	}
	return id, nil
}
