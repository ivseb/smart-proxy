package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"smart-proxy/internal/guard"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
	"smart-proxy/internal/vault"
)

// Security regressions found in review: each request below once got through, or leaked.

func TestOpenPathsCantBeUsedToReachProtectedOnes(t *testing.T) {
	h, tr, _, _ := protectedHandler(t, true)
	for _, path := range []string{"/health/..;/admin", "/health/%2e%2e;/admin", "/health/..%3B/admin", "/saml/acs/..;/admin",
		"/health/..%2Fadmin", "/health/..\\admin", "/health;x/../admin"} {
		tr.got = nil
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://web.example.com"+path, nil)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || tr.got != nil {
			t.Errorf("%s: %d, reached the app: %v", path, w.Code, tr.got != nil)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://web.example.com/__smart_proxy/status?path=/health/../admin", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status endpoint with a path climbing out of an open one: %d", w.Code)
	}
}

func TestNoOpenRedirectAfterSignIn(t *testing.T) {
	h, _, _, _ := protectedHandler(t, true)
	for _, next := range []string{"//evil.com", "/\t/evil.com", "/./\\evil.com", "/\\evil.com", "https://evil.com", "/\r\n/evil.com", "javascript:alert(1)"} {
		form := url.Values{"password": {"s3cret-pass"}, "next": {next}}
		r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q redirected to %q", next, loc)
		}
	}
}

func TestSignInFromAnotherSiteIsRefused(t *testing.T) {
	h, _, _, _ := protectedHandler(t, true)
	r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader("password=s3cret-pass"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site sign-in: %d", w.Code)
	}
}

func TestGuessingIsLimitedWhateverTheClientClaims(t *testing.T) {
	h, _, _, _ := protectedHandler(t, true)
	trusted, _ := traffic.ParseTrustedProxies(traffic.DefaultTrustedProxies)
	h.TrustedProxies = trusted

	// Basic credentials: after 10 wrong guesses, even the right password is refused for a while.
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("GET", "http://web.example.com/api", nil)
		r.SetBasicAuth("team", "wrong-guess")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	r := httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.SetBasicAuth("team", "s3cret-pass")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Basic guessing not limited: %d", w.Code)
	}

	// The form, from a private network, with a forged X-Forwarded-For each time.
	last := 0
	for i := 0; i < 12; i++ {
		r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader("password=nope-"+string(rune('a'+i))))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "10.128.0.5:4000" // The router
		r.Header.Set("X-Forwarded-For", "10.99.0."+string(rune('1'+i%9))+", 10.20.30.40")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For escaped the limit: %d", last)
	}
}

func TestSmartProxyCookiesAndUserHeaderNeverLeak(t *testing.T) {
	h, tr, token, _ := protectedHandler(t, true)
	h.store.AddRoute(&store.RouteConfig{ID: "ing-legacy", Host: "web.example.com", Path: "/legacy", Namespace: ns, Deployment: "web", TargetService: "web", TargetPort: 8080})

	cookie := "a=1; sp_auth_12345678=stolen-session; json={\"x\":1}; sp2=x y"
	for _, path := range []string{"/legacy/x", "/health"} { // Another app, an open path
		r := httptest.NewRequest("GET", "http://web.example.com"+path, nil)
		r.Header.Set("Cookie", cookie)
		r.Header.Set("X_Smart_Proxy_User", "admin")
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got := tr.got.Get("Cookie"); got != `a=1; json={"x":1}; sp2=x y` {
			t.Errorf("%s: app got cookies %q", path, got)
		}
		if tr.got.Get("X_Smart_Proxy_User") != "" || tr.got.Get(guard.UserHeader) != "" {
			t.Errorf("%s: forged user reached the app: %v", path, tr.got)
		}
	}

	r := httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Connection", "X-Smart-Proxy-User")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if tr.got.Get(guard.UserHeader) != "token:jenkins" {
		t.Fatalf("a Connection header removed the user: %v", tr.got)
	}
}

func TestChangingThePasswordSignsEveryoneOut(t *testing.T) {
	h, tr, _, _ := protectedHandler(t, true)
	form := url.Values{"password": {"s3cret-pass"}}
	r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	session := w.Result().Cookies()[0]

	hash, _ := guard.HashPassword("new-password-1")
	h.Guard.Vault.UpdateCredentials(context.Background(), "ing-web", func(c *vault.Credentials) {
		c.Users["team"] = vault.Secret{Hash: hash}
	})
	tr.got = nil
	r = httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || tr.got != nil {
		t.Fatalf("old session after a password change: %d", w.Code)
	}
}

// The status endpoint must not wake a backend Smart Proxy doesn't manage (a pin to it can be
// had by anyone, with the link).
func TestStatusNeverWakesUnmanagedBackends(t *testing.T) {
	h, c, _ := webHandler(t, []*store.RouteConfig{samlRoute(false)}, ready("app-a", 1), sleeping("app-b"))
	r := httptest.NewRequest("GET", "http://portal.example.com/__smart_proxy/status?path=/", nil)
	r.AddCookie(&http.Cookie{Name: stickyCookie("ing-portal"), Value: "app-b"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if replicas(t, c, "app-b") != 0 || !strings.Contains(w.Body.String(), "waiting") {
		t.Fatalf("status woke unmanaged app-b: %s", w.Body.String())
	}
}
