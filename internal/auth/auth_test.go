package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// newTestServer serves a protected /api/ping (echoing the user) behind the auth middleware.
func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = time.Hour
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong:" + UserFromContext(r.Context())))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("dashboard")) })
	srv := httptest.NewServer(a.Wrap(mux))
	t.Cleanup(srv.Close)
	return srv
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func do(t *testing.T, client *http.Client, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, b.String()
}

func get(t *testing.T, client *http.Client, url string, mutate ...func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for _, m := range mutate {
		m(req)
	}
	return do(t, client, req)
}

func TestModeNoneAllowsEverything(t *testing.T) {
	srv := newTestServer(t, Config{Mode: ModeNone})
	resp, body := get(t, srv.Client(), srv.URL+"/api/ping")
	if resp.StatusCode != http.StatusOK || body != "pong:" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

func TestBasicAuth(t *testing.T) {
	srv := newTestServer(t, Config{Mode: ModeBasic, BasicUsername: "admin", BasicPassword: "s3cret"})

	resp, _ := get(t, srv.Client(), srv.URL+"/")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous: got %d, WWW-Authenticate=%q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	resp, _ = get(t, srv.Client(), srv.URL+"/api/ping", func(r *http.Request) { r.SetBasicAuth("admin", "wrong") })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d", resp.StatusCode)
	}

	resp, body := get(t, srv.Client(), srv.URL+"/api/ping", func(r *http.Request) { r.SetBasicAuth("admin", "s3cret") })
	if resp.StatusCode != http.StatusOK || body != "pong:admin" {
		t.Fatalf("valid credentials: got %d %q", resp.StatusCode, body)
	}
}

func TestTokenBearerAndLogin(t *testing.T) {
	srv := newTestServer(t, Config{Mode: ModeToken, Token: "tok-123"})
	client := noRedirectClient()

	// Browsers without a session are sent to the login page; API calls get 401.
	resp, _ := get(t, client, srv.URL+"/")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/auth/login" {
		t.Fatalf("anonymous page: got %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = get(t, client, srv.URL+"/api/ping")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous api: got %d", resp.StatusCode)
	}

	resp, body := get(t, client, srv.URL+"/api/ping", func(r *http.Request) { r.Header.Set("Authorization", "Bearer tok-123") })
	if resp.StatusCode != http.StatusOK || body != "pong:token" {
		t.Fatalf("bearer: got %d %q", resp.StatusCode, body)
	}
	resp, _ = get(t, client, srv.URL+"/api/ping", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad bearer: got %d", resp.StatusCode)
	}

	login := func(token string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", srv.URL)
		resp, _ := do(t, client, req)
		return resp
	}
	if resp := login("nope"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d", resp.StatusCode)
	}
	resp = login("tok-123")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: got %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	resp, body = get(t, client, srv.URL+"/api/ping", func(r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
	})
	if resp.StatusCode != http.StatusOK || body != "pong:token" {
		t.Fatalf("session: got %d %q", resp.StatusCode, body)
	}
}

func TestSessionCookieTamperingAndExpiry(t *testing.T) {
	a, err := New(Config{Mode: ModeToken, Token: "t", SessionTTL: time.Hour, SessionSecret: "k"})
	if err != nil {
		t.Fatal(err)
	}
	withCookie := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		return r
	}

	valid, _ := a.signer.sign(session{User: "alice", Expires: time.Now().Add(time.Hour).Unix()})
	if user, ok := a.sessionUser(withCookie(valid)); !ok || user != "alice" {
		t.Fatalf("valid session rejected: %q %v", user, ok)
	}

	forged, _ := signer{key: []byte("other")}.sign(session{User: "mallory", Expires: time.Now().Add(time.Hour).Unix()})
	if _, ok := a.sessionUser(withCookie(forged)); ok {
		t.Fatal("session signed with another key accepted")
	}

	expired, _ := a.signer.sign(session{User: "alice", Expires: time.Now().Add(-time.Minute).Unix()})
	if _, ok := a.sessionUser(withCookie(expired)); ok {
		t.Fatal("expired session accepted")
	}
}

func TestHeaderMode(t *testing.T) {
	srv := newTestServer(t, Config{Mode: ModeHeader, HeaderUser: "X-Forwarded-User"})

	resp, _ := get(t, srv.Client(), srv.URL+"/api/ping")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing header: got %d", resp.StatusCode)
	}
	resp, body := get(t, srv.Client(), srv.URL+"/api/ping", func(r *http.Request) { r.Header.Set("X-Forwarded-User", "bob") })
	if resp.StatusCode != http.StatusOK || body != "pong:bob" {
		t.Fatalf("with header: got %d %q", resp.StatusCode, body)
	}
}

func TestCrossOriginWritesRejected(t *testing.T) {
	srv := newTestServer(t, Config{Mode: ModeBasic, BasicUsername: "admin", BasicPassword: "pw"})
	post := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/ping", nil)
		req.SetBasicAuth("admin", "pw")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, _ := do(t, srv.Client(), req)
		return resp.StatusCode
	}
	if code := post("https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: got %d", code)
	}
	if code := post(srv.URL); code != http.StatusOK {
		t.Fatalf("same-origin POST: got %d", code)
	}
	if code := post(""); code != http.StatusOK {
		t.Fatalf("non-browser POST: got %d", code)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	cases := map[string]map[string]string{
		"unknown mode":          {"AUTH_MODE": "magic"},
		"basic without pass":    {"AUTH_MODE": "basic"},
		"token without token":   {"AUTH_MODE": "token"},
		"oidc missing settings": {"AUTH_MODE": "oidc", "AUTH_OIDC_ISSUER_URL": "https://idp"},
		"oidc without allow":    {"AUTH_MODE": "oidc", "AUTH_OIDC_ISSUER_URL": "https://idp", "AUTH_OIDC_CLIENT_ID": "c", "AUTH_OIDC_CLIENT_SECRET": "s", "AUTH_OIDC_REDIRECT_URL": "https://a/auth/callback"},
		"bad ttl":               {"AUTH_MODE": "none", "AUTH_SESSION_TTL": "forever"},
	}
	for name, kv := range cases {
		if _, err := LoadConfig(env(kv)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	cfg, err := LoadConfig(env(map[string]string{}))
	if err != nil || cfg.Mode != ModeNone {
		t.Fatalf("default: %v %v", cfg.Mode, err)
	}
	cfg, err = LoadConfig(env(map[string]string{"AUTH_MODE": "OIDC", "AUTH_OIDC_ISSUER_URL": "https://idp", "AUTH_OIDC_CLIENT_ID": "c", "AUTH_OIDC_CLIENT_SECRET": "s", "AUTH_OIDC_REDIRECT_URL": "https://a/auth/callback", "AUTH_OIDC_ALLOWED_GROUPS": "ops, admins"}))
	if err != nil || len(cfg.OIDCAllowedGroups) != 2 {
		t.Fatalf("oidc: %+v %v", cfg.OIDCAllowedGroups, err)
	}
}

// fakeIdP is a minimal OpenID Connect provider for exercising the full login flow.
type fakeIdP struct {
	*httptest.Server
	key       *rsa.PrivateKey
	claims    map[string]any // extra claims for the next id_token
	nonce     string
	challenge string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key}
	mux := http.NewServeMux()
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.URL,
			"authorization_endpoint":                idp.URL + "/authorize",
			"token_endpoint":                        idp.URL + "/token",
			"jwks_uri":                              idp.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != idp.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims := map[string]any{
			"iss": idp.URL, "aud": "smart-proxy", "sub": "user-1", "nonce": idp.nonce,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		for k, v := range idp.claims {
			claims[k] = v
		}
		payload, _ := json.Marshal(claims)
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		jws, _ := sig.Sign(payload)
		idToken, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	})
	return idp
}

// login runs /auth/login -> IdP -> /auth/callback and returns the callback response.
func (idp *fakeIdP) login(t *testing.T, srv *httptest.Server, claims map[string]any) *http.Response {
	t.Helper()
	client := noRedirectClient()
	idp.claims = claims

	resp, _ := get(t, client, srv.URL+"/auth/login")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login: got %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	q := authURL.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE not used: %s", authURL)
	}
	idp.nonce, idp.challenge = q.Get("nonce"), q.Get("code_challenge")
	stateCookies := resp.Cookies()

	cb := srv.URL + "/auth/callback?" + url.Values{"code": {"c0de"}, "state": {q.Get("state")}}.Encode()
	resp, _ = get(t, client, cb, func(r *http.Request) {
		for _, c := range stateCookies {
			r.AddCookie(c)
		}
	})
	return resp
}

func oidcConfig(issuer string) Config {
	return Config{
		Mode: ModeOIDC, OIDCIssuerURL: issuer, OIDCClientID: "smart-proxy", OIDCClientSecret: "secret",
		OIDCRedirectURL: "http://dashboard/auth/callback", OIDCScopes: []string{"openid", "email"},
		OIDCUsernameClaim: "email", OIDCGroupsClaim: "groups", SessionTTL: time.Hour,
	}
}

func TestOIDCLoginFlow(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := oidcConfig(idp.URL)
	cfg.OIDCAllowedDomains = []string{"example.com"}
	cfg.OIDCAllowedGroups = []string{"smart-proxy-admins"}
	srv := newTestServer(t, cfg)

	cases := []struct {
		name    string
		claims  map[string]any
		allowed bool
	}{
		{"allowed domain", map[string]any{"email": "alice@example.com", "email_verified": true}, true},
		{"unverified email", map[string]any{"email": "eve@example.com", "email_verified": false}, false},
		{"other domain", map[string]any{"email": "bob@other.org"}, false},
		{"allowed group", map[string]any{"email": "carol@other.org", "groups": []string{"devs", "smart-proxy-admins"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := idp.login(t, srv, tc.claims)
			if !tc.allowed {
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("expected 403, got %d", resp.StatusCode)
				}
				return
			}
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("callback: got %d", resp.StatusCode)
			}
			session := resp.Cookies()
			r, body := get(t, srv.Client(), srv.URL+"/api/ping", func(r *http.Request) {
				for _, c := range session {
					r.AddCookie(c)
				}
			})
			if r.StatusCode != http.StatusOK || body != "pong:"+tc.claims["email"].(string) {
				t.Fatalf("api with session: got %d %q", r.StatusCode, body)
			}
		})
	}
}

func TestOIDCCallbackRejectsForgedState(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := oidcConfig(idp.URL)
	cfg.OIDCAllowAll = true
	srv := newTestServer(t, cfg)
	client := noRedirectClient()

	resp, _ := get(t, client, srv.URL+"/auth/login")
	stateCookies := resp.Cookies()
	resp, _ = get(t, client, srv.URL+"/auth/callback?code=c0de&state=attacker", func(r *http.Request) {
		for _, c := range stateCookies {
			r.AddCookie(c)
		}
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged state: got %d", resp.StatusCode)
	}

	resp, _ = get(t, client, srv.URL+"/auth/callback?code=c0de&state=x")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing state cookie: got %d", resp.StatusCode)
	}
}
