package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cistern/internal/server/oidc/oidctest"
)

func TestOIDCLogin(t *testing.T) {
	idp := oidctest.New("cistern", secrets["OIDC_SECRET"])
	defer idp.Close()
	e := newEnv(t, noDaemon, nil, func(s string) string {
		return strings.Replace(s, "https://idp.invalid", idp.Issuer(), 1)
	})

	login := func(groups []string) (*http.Response, string) {
		t.Helper()
		idp.SetClaims(map[string]any{"sub": "sub-42", "email": "olga@example.com", "name": "Olga", "groups": groups})
		resp, _ := e.raw("GET", "/api/v1/auth/login?return_to=/jobs/app", nil, nil)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("login: %d", resp.StatusCode)
		}
		var loginCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "cistern_login" {
				loginCookie = c
			}
		}
		if loginCookie == nil || loginCookie.SameSite != http.SameSiteLaxMode || !loginCookie.HttpOnly {
			t.Fatalf("login cookie: %+v", loginCookie)
		}
		authURL, _ := url.Parse(resp.Header.Get("Location"))
		q := authURL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" || !strings.HasSuffix(q.Get("redirect_uri"), "/api/v1/auth/callback") {
			t.Fatalf("authorize url: %s", authURL)
		}
		ar, err := noRedirect.Get(authURL.String())
		if err != nil {
			t.Fatal(err)
		}
		ar.Body.Close()
		cb, _ := url.Parse(ar.Header.Get("Location"))
		bad, _ := e.raw("GET", "/api/v1/auth/callback?code="+cb.Query().Get("code")+"&state=wrong", nil, map[string]string{"Cookie": "cistern_login=" + loginCookie.Value})
		if loc := bad.Header.Get("Location"); !strings.HasPrefix(loc, "/denied") {
			t.Fatalf("wrong state accepted: %s", loc)
		}
		resp, _ = e.raw("GET", "/api/v1/auth/callback?"+cb.RawQuery, nil, map[string]string{"Cookie": "cistern_login=" + loginCookie.Value})
		session := ""
		for _, c := range resp.Cookies() {
			if c.Name == "cistern_session" {
				session = c.Value
				if c.SameSite != http.SameSiteStrictMode || !c.HttpOnly {
					t.Fatalf("session cookie: %+v", c)
				}
			}
		}
		return resp, session
	}

	resp, session := login([]string{"ops", "other"})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/jobs/app" || session == "" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	u := &user{e: e, cookie: session}
	u.json("GET", "/api/v1/me", nil, 200, &u.me)
	if u.me.User.ID != "sub-42" || u.me.User.Email != "olga@example.com" || strings.Join(u.me.Roles, ",") != "viewer,operator" {
		t.Fatalf("me: %+v", u.me)
	}
	if len(e.auditEntries(ActionLogin)) != 1 {
		t.Fatal("auth.login not audited")
	}

	resp, session = login([]string{"nobody"})
	if resp.Header.Get("Location") != "/denied?reason=no_role" || session != "" {
		t.Fatalf("no role: %s %q", resp.Header.Get("Location"), session)
	}
	if d := e.auditEntries(ActionAuthDenied); len(d) != 1 || d[0].Actor.Email != "olga@example.com" {
		t.Fatalf("auth.denied: %+v", d)
	}
}
