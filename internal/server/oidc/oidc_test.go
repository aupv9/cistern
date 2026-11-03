package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"cistern/internal/server/oidc/oidctest"
)

const (
	clientID     = "cistern-ui"
	clientSecret = "super-secret-client-value"
	redirectURL  = "https://backup.example.com/api/v1/auth/callback"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*oidctest.IdP, *Provider, *clock) {
	t.Helper()
	idp := oidctest.New(clientID, clientSecret)
	t.Cleanup(idp.Close)
	c := &clock{t: time.Now()}
	p := New(Config{Issuer: idp.Issuer(), ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		Scopes: []string{"profile", "email", "openid"}, Now: c.now})
	return idp, p, c
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func login(t *testing.T, p *Provider) (*Identity, error) {
	t.Helper()
	ctx := context.Background()
	ar, err := NewAuthRequest()
	if err != nil {
		t.Fatal(err)
	}
	u, err := p.AuthURL(ctx, ar)
	if err != nil {
		t.Fatal(err)
	}
	q := mustQuery(t, u)
	if q.Get("code_challenge") != s256(ar.Verifier) || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("bad PKCE parameters: %v", q)
	}
	if !strings.HasPrefix(q.Get("scope"), "openid") || q.Get("state") != ar.State || q.Get("nonce") != ar.Nonce || q.Get("redirect_uri") != redirectURL {
		t.Fatalf("bad authorize parameters: %v", q)
	}
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %s", resp.Status)
	}
	cb := mustQuery(t, resp.Header.Get("Location"))
	if cb.Get("state") != ar.State {
		t.Fatal("state not returned")
	}
	return p.Exchange(ctx, cb.Get("code"), ar)
}

func mustQuery(t *testing.T, s string) url.Values {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func s256(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestScopes(t *testing.T) {
	_, p, _ := setup(t)
	u, err := p.AuthURL(context.Background(), AuthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustQuery(t, u).Get("scope"); got != "openid profile email" {
		t.Fatalf("scope %q", got)
	}
}

func TestNewAuthRequest(t *testing.T) {
	a, err := NewAuthRequest()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewAuthRequest()
	for _, v := range []string{a.State, a.Nonce, a.Verifier} {
		if len(v) != 43 || strings.ContainsAny(v, "+/=") {
			t.Errorf("bad random value %q", v)
		}
	}
	if a.State == b.State || a.State == a.Nonce || a.Nonce == a.Verifier {
		t.Error("values not unique")
	}
}

func TestFullFlow(t *testing.T) {
	for _, es := range []bool{false, true} {
		name := "RS256"
		if es {
			name = "ES256"
		}
		t.Run(name, func(t *testing.T) {
			idp, p, _ := setup(t)
			idp.UseES256(es)
			idp.SetClaims(map[string]any{"sub": "u-42", "email": "a@example.com", "name": "Alice", "groups": []string{"ops", "dba"}})
			id, err := login(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if id.Subject != "u-42" || id.Email != "a@example.com" || id.Name != "Alice" || strings.Join(id.Groups, ",") != "ops,dba" {
				t.Fatalf("identity: %+v", id)
			}
			if idp.LastAuthMethod() != "client_secret_basic" {
				t.Errorf("auth method %q", idp.LastAuthMethod())
			}
		})
	}
}

func TestClaimMappingAndFallbacks(t *testing.T) {
	idp := oidctest.New(clientID, "")
	defer idp.Close()
	p := New(Config{Issuer: idp.Issuer(), ClientID: clientID, RedirectURL: redirectURL, GroupsClaim: "roles", EmailClaim: "upn", NameClaim: "display"})
	idp.SetClaims(map[string]any{"sub": "s1", "email": nil, "name": nil, "upn": "s1@corp", "roles": "backup-admins"})
	id, err := login(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "s1@corp" || id.Name != "s1@corp" || len(id.Groups) != 1 || id.Groups[0] != "backup-admins" {
		t.Fatalf("identity: %+v", id)
	}
	if idp.LastAuthMethod() != "none" {
		t.Errorf("public client auth method %q", idp.LastAuthMethod())
	}
	idp.SetClaims(map[string]any{"sub": "s2", "email": nil, "name": nil})
	id, err = login(t, p)
	if err != nil || id.Name != "s2" {
		t.Fatalf("name fallback: %+v %v", id, err)
	}
}

func TestClientSecretPost(t *testing.T) {
	idp, p, _ := setup(t)
	idp.SetAuthMethods([]string{"client_secret_post"})
	if _, err := login(t, p); err != nil {
		t.Fatal(err)
	}
	if idp.LastAuthMethod() != "client_secret_post" {
		t.Errorf("auth method %q", idp.LastAuthMethod())
	}
}

func TestWrongClientSecret(t *testing.T) {
	idp := oidctest.New(clientID, clientSecret)
	defer idp.Close()
	p := New(Config{Issuer: idp.Issuer(), ClientID: clientID, ClientSecret: "wrong-secret-value", RedirectURL: redirectURL})
	_, err := login(t, p)
	if err == nil || errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "wrong-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodeIsSingleUseAndBoundToVerifier(t *testing.T) {
	_, p, _ := setup(t)
	ctx := context.Background()
	ar, _ := NewAuthRequest()
	u, _ := p.AuthURL(ctx, ar)
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	code := mustQuery(t, resp.Header.Get("Location")).Get("code")
	bad := ar
	bad.Verifier = "another-verifier-another-verifier-another-ver"
	_, err = p.Exchange(ctx, code, bad)
	if err == nil || strings.Contains(err.Error(), code) {
		t.Fatalf("wrong verifier: %v", err)
	}
	if _, err := p.Exchange(ctx, code, ar); err == nil {
		t.Fatal("code reused")
	}
}

func TestKeyRotation(t *testing.T) {
	idp, p, c := setup(t)
	ctx := context.Background()
	nonce := "n-1"
	if _, err := p.VerifyIDToken(ctx, idp.Sign(idp.StandardClaims(nonce)), nonce); err != nil {
		t.Fatal(err)
	}
	if idp.JWKSHits() != 1 {
		t.Fatalf("jwks hits %d", idp.JWKSHits())
	}
	if _, err := p.VerifyIDToken(ctx, idp.Sign(idp.StandardClaims(nonce)), nonce); err != nil || idp.JWKSHits() != 1 {
		t.Fatalf("cached key not used: %v hits=%d", err, idp.JWKSHits())
	}
	idp.RotateKey()
	tok := idp.Sign(idp.StandardClaims(nonce))
	if _, err := p.VerifyIDToken(ctx, tok, nonce); err == nil || idp.JWKSHits() != 1 {
		t.Fatalf("refetch within 30s: %v hits=%d", err, idp.JWKSHits())
	}
	c.add(31 * time.Second)
	if _, err := p.VerifyIDToken(ctx, tok, nonce); err != nil || idp.JWKSHits() != 2 {
		t.Fatalf("unknown kid did not refetch: %v hits=%d", err, idp.JWKSHits())
	}
}

func TestKeyCacheExpires(t *testing.T) {
	idp, p, c := setup(t)
	ctx := context.Background()
	if _, err := p.VerifyIDToken(ctx, idp.Sign(idp.StandardClaims("n")), "n"); err != nil {
		t.Fatal(err)
	}
	c.add(61 * time.Minute)
	claims := idp.StandardClaims("n")
	claims["iat"] = c.now().Unix()
	claims["exp"] = c.now().Add(time.Minute).Unix()
	if _, err := p.VerifyIDToken(ctx, idp.Sign(claims), "n"); err != nil {
		t.Fatal(err)
	}
	if idp.JWKSHits() != 2 {
		t.Fatalf("stale JWKS not refreshed: hits=%d", idp.JWKSHits())
	}
}

func TestRejections(t *testing.T) {
	idp, p, _ := setup(t)
	ctx := context.Background()
	nonce := "the-nonce"
	if _, err := p.VerifyIDToken(ctx, idp.Sign(idp.StandardClaims(nonce)), nonce); err != nil {
		t.Fatal(err)
	}
	mod := func(f func(map[string]any)) map[string]any {
		c := idp.StandardClaims(nonce)
		f(c)
		return c
	}
	now := time.Now()
	cases := map[string]string{
		"alg none":            idp.SignWith("none", idp.StandardClaims(nonce)),
		"HS256 client secret": idp.SignWith("HS256", idp.StandardClaims(nonce)),
		"wrong iss":           idp.Sign(mod(func(c map[string]any) { c["iss"] = "https://evil.example.com" })),
		"wrong aud":           idp.Sign(mod(func(c map[string]any) { c["aud"] = "other-client" })),
		"multi aud no azp":    idp.Sign(mod(func(c map[string]any) { c["aud"] = []string{clientID, "other"} })),
		"azp mismatch":        idp.Sign(mod(func(c map[string]any) { c["aud"] = []string{clientID, "other"}; c["azp"] = "other" })),
		"expired":             idp.Sign(mod(func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() })),
		"no exp":              idp.Sign(mod(func(c map[string]any) { delete(c, "exp") })),
		"iat in future":       idp.Sign(mod(func(c map[string]any) { c["iat"] = now.Add(5 * time.Minute).Unix() })),
		"wrong nonce":         idp.Sign(mod(func(c map[string]any) { c["nonce"] = "other" })),
		"missing nonce":       idp.Sign(mod(func(c map[string]any) { delete(c, "nonce") })),
		"missing sub":         idp.Sign(mod(func(c map[string]any) { delete(c, "sub") })),
		"malformed":           "abc.def",
		"garbage":             "a.b.c",
	}
	good := idp.Sign(idp.StandardClaims(nonce))
	parts := strings.Split(good, ".")
	evil := mod(func(c map[string]any) { c["sub"] = "admin" })
	other := idp.Sign(evil)
	cases["tampered payload"] = parts[0] + "." + strings.Split(other, ".")[1] + "." + parts[2]
	idp.UseES256(true)
	es := idp.Sign(idp.StandardClaims(nonce))
	esParts := strings.Split(es, ".")
	cases["ES alg with RSA kid"] = esParts[0] + "." + esParts[1] + "." + parts[2]
	idp.UseES256(false)

	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			id, err := p.VerifyIDToken(ctx, tok, nonce)
			if err == nil {
				t.Fatalf("accepted: %+v", id)
			}
			if strings.Contains(err.Error(), clientSecret) || strings.Contains(err.Error(), tok) {
				t.Fatalf("error leaks secret or token: %v", err)
			}
		})
	}
	if _, err := p.VerifyIDToken(ctx, good, ""); err == nil {
		t.Error("empty expected nonce accepted")
	}
	slight := mod(func(c map[string]any) {
		c["exp"] = now.Add(-30 * time.Second).Unix()
		c["iat"] = now.Add(30 * time.Second).Unix()
	})
	if _, err := p.VerifyIDToken(ctx, idp.Sign(slight), nonce); err != nil {
		t.Errorf("small clock skew rejected: %v", err)
	}
	multi := mod(func(c map[string]any) { c["aud"] = []string{"other", clientID}; c["azp"] = clientID })
	if _, err := p.VerifyIDToken(ctx, idp.Sign(multi), nonce); err != nil {
		t.Errorf("multi aud with azp rejected: %v", err)
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	idp, p, _ := setup(t)
	idp.SetDiscoveryIssuer("https://other.example.com")
	_, err := p.AuthURL(context.Background(), AuthRequest{})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("err = %v", err)
	}
	idp.SetDiscoveryIssuer("")
	if _, err := p.AuthURL(context.Background(), AuthRequest{}); err != nil {
		t.Fatalf("discovery not retried: %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	idp := oidctest.New(clientID, clientSecret)
	issuer := idp.Issuer()
	idp.Close()
	p := New(Config{Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL})
	if _, err := p.AuthURL(context.Background(), AuthRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	ar, _ := NewAuthRequest()
	_, err := p.Exchange(context.Background(), "some-code-value", ar)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), clientSecret) || strings.Contains(err.Error(), "some-code-value") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSRequiredForHTTPSIssuer(t *testing.T) {
	p := New(Config{Issuer: "https://idp.example.com"})
	if err := p.checkEndpoint("token_endpoint", "http://idp.example.com/token"); err == nil {
		t.Fatal("http endpoint accepted for https issuer")
	}
	if err := p.checkEndpoint("token_endpoint", "https://idp.example.com/token"); err != nil {
		t.Fatal(err)
	}
}

func TestParseKeyRejectsWeakKeys(t *testing.T) {
	small := base64.RawURLEncoding.EncodeToString(make([]byte, 128))
	if _, ok := parseKey(jwk{Kty: "RSA", N: "AQ" + small, E: "AQAB"}); ok {
		t.Error("1024-bit RSA key accepted")
	}
	if _, ok := parseKey(jwk{Kty: "EC", Crv: "P-384", X: "AA", Y: "AA"}); ok {
		t.Error("P-384 key accepted")
	}
	zero := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, ok := parseKey(jwk{Kty: "EC", Crv: "P-256", X: zero, Y: zero}); ok {
		t.Error("point not on curve accepted")
	}
	if _, ok := parseKey(jwk{Kty: "oct", Use: "sig"}); ok {
		t.Error("symmetric key accepted")
	}
}
