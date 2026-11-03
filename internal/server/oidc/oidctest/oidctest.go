package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

type grant struct {
	nonce, challenge, redirect string
	claims                     map[string]any
}

type IdP struct {
	srv                    *httptest.Server
	clientID, clientSecret string

	mu              sync.Mutex
	gen             int
	rsaKey          *rsa.PrivateKey
	ecKey           *ecdsa.PrivateKey
	es              bool
	extra           map[string]any
	codes           map[string]grant
	jwksHits        int
	authMethods     []string
	discoveryIssuer string
	lastAuthMethod  string
}

func New(clientID, clientSecret string) *IdP {
	i := &IdP{clientID: clientID, clientSecret: clientSecret, codes: map[string]grant{}}
	i.RotateKey()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", i.discovery)
	mux.HandleFunc("GET /authorize", i.authorize)
	mux.HandleFunc("POST /token", i.token)
	mux.HandleFunc("GET /jwks", i.jwks)
	i.srv = httptest.NewServer(mux)
	return i
}

func (i *IdP) Issuer() string { return i.srv.URL }

func (i *IdP) Close() { i.srv.Close() }

func (i *IdP) SetClaims(extra map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.extra = map[string]any{}
	for k, v := range extra {
		i.extra[k] = v
	}
}

func (i *IdP) UseES256(on bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.es = on
}

func (i *IdP) SetAuthMethods(methods []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.authMethods = methods
}

func (i *IdP) SetDiscoveryIssuer(iss string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.discoveryIssuer = iss
}

func (i *IdP) JWKSHits() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.jwksHits
}

func (i *IdP) LastAuthMethod() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastAuthMethod
}

func (i *IdP) RotateKey() {
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.gen++
	i.rsaKey, i.ecKey = rk, ek
}

func (i *IdP) kid(alg string) string { return fmt.Sprintf("k%d-%s", i.gen, alg) }

func (i *IdP) StandardClaims(nonce string) map[string]any {
	now := time.Now()
	c := map[string]any{
		"iss":    i.Issuer(),
		"aud":    i.clientID,
		"sub":    "user-1",
		"email":  "user@example.com",
		"name":   "User One",
		"groups": []string{"admins"},
		"iat":    now.Unix(),
		"exp":    now.Add(5 * time.Minute).Unix(),
		"nonce":  nonce,
	}
	i.mu.Lock()
	for k, v := range i.extra {
		c[k] = v
	}
	i.mu.Unlock()
	return c
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (i *IdP) Sign(claims map[string]any) string {
	i.mu.Lock()
	alg := "RS256"
	if i.es {
		alg = "ES256"
	}
	i.mu.Unlock()
	return i.SignWith(alg, claims)
}

func (i *IdP) SignWith(alg string, claims map[string]any) string {
	i.mu.Lock()
	rk, ek, kid := i.rsaKey, i.ecKey, i.kid(alg)
	i.mu.Unlock()
	hdr := map[string]any{"alg": alg, "typ": "JWT"}
	if alg == "RS256" || alg == "ES256" {
		hdr["kid"] = kid
	}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	input := b64(hb) + "." + b64(pb)
	h := sha256.Sum256([]byte(input))
	var sig []byte
	switch alg {
	case "RS256":
		s, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, h[:])
		if err != nil {
			panic(err)
		}
		sig = s
	case "ES256":
		r, s, err := ecdsa.Sign(rand.Reader, ek, h[:])
		if err != nil {
			panic(err)
		}
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
	case "HS256":
		m := hmac.New(sha256.New, []byte(i.clientSecret))
		m.Write([]byte(input))
		sig = m.Sum(nil)
	case "none":
	default:
		panic("oidctest: unsupported alg " + alg)
	}
	return input + "." + b64(sig)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (i *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	iss, methods := i.discoveryIssuer, i.authMethods
	i.mu.Unlock()
	if iss == "" {
		iss = i.Issuer()
	}
	if methods == nil {
		methods = []string{"client_secret_basic", "client_secret_post"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                i.Issuer() + "/authorize",
		"token_endpoint":                        i.Issuer() + "/token",
		"jwks_uri":                              i.Issuer() + "/jwks",
		"response_types_supported":              []string{"code"},
		"id_token_signing_alg_values_supported": []string{"RS256", "ES256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": methods,
	})
}

func (i *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("client_id") != i.clientID:
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	case redirect == "":
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	case q.Get("response_type") != "code":
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		http.Error(w, "PKCE S256 required", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	var b [16]byte
	rand.Read(b[:])
	code := hex.EncodeToString(b[:])
	claims := i.StandardClaims(q.Get("nonce"))
	i.mu.Lock()
	i.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: redirect, claims: claims}
	i.mu.Unlock()
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (i *IdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	method := "client_secret_basic"
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		method = "client_secret_post"
		if secret == "" {
			method = "none"
		}
	}
	i.mu.Lock()
	i.lastAuthMethod = method
	i.mu.Unlock()
	if id != i.clientID || (i.clientSecret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(i.clientSecret)) != 1) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	code := r.PostForm.Get("code")
	i.mu.Lock()
	g, ok := i.codes[code]
	delete(i.codes, code)
	i.mu.Unlock()
	if !ok || g.redirect != r.PostForm.Get("redirect_uri") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "access-" + code,
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     i.Sign(g.claims),
	})
}

func pad(b []byte, n int) []byte {
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

func (i *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	i.jwksHits++
	rk, ek := i.rsaKey, i.ecKey
	rkid, ekid := i.kid("RS256"), i.kid("ES256")
	i.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{
		{"kty": "RSA", "kid": rkid, "use": "sig", "alg": "RS256", "n": b64(rk.N.Bytes()), "e": b64(big.NewInt(int64(rk.E)).Bytes())},
		{"kty": "EC", "kid": ekid, "use": "sig", "alg": "ES256", "crv": "P-256", "x": b64(pad(ek.X.Bytes(), 32)), "y": b64(pad(ek.Y.Bytes(), 32))},
		{"kty": "RSA", "kid": "enc-key", "use": "enc", "n": b64(rk.N.Bytes()), "e": "AQAB"},
	}})
}
