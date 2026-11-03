package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"cistern/internal/server/oidc"
)

type sealer struct{ aead cipher.AEAD }

func newSealer(secret []byte, purpose string) (*sealer, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, "cistern "+purpose+" v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(v any, aad string) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, data, []byte(aad))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *sealer) open(token, aad string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return errors.New("malformed")
	}
	n := s.aead.NonceSize()
	data, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return errors.New("invalid")
	}
	return json.Unmarshal(data, v)
}

type session struct {
	ID      string   `json:"sid"`
	Subject string   `json:"sub"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name"`
	Roles   []string `json:"roles"`
	CSRF    string   `json:"csrf"`
	Issued  int64    `json:"iat"`
	Expires int64    `json:"exp"`
	Last    int64    `json:"last"`
}

func (s *session) expiresAt() time.Time { return time.Unix(s.Expires, 0).UTC() }

type loginState struct {
	Request  oidc.AuthRequest `json:"req"`
	ReturnTo string           `json:"return_to"`
	Expires  int64            `json:"exp"`
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func equalSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1 && a != ""
}

type cookieNames struct {
	session, login string
	secure         bool
}

func newCookieNames(dev bool) cookieNames {
	if dev {
		return cookieNames{session: "cistern_session", login: "cistern_login"}
	}
	return cookieNames{session: "__Host-cistern_session", login: "__Host-cistern_login", secure: true}
}

func (c cookieNames) set(w http.ResponseWriter, name, value string, maxAge time.Duration, sameSite http.SameSite) {
	ck := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: sameSite}
	if maxAge < 0 {
		ck.MaxAge = -1
	} else {
		ck.MaxAge = int(maxAge / time.Second)
	}
	http.SetCookie(w, ck)
}

type revocations struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (r *revocations) add(id string, until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range r.m {
		if now.After(t) {
			delete(r.m, k)
		}
	}
	r.m[id] = until
}

func (r *revocations) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[id]
	return ok
}
