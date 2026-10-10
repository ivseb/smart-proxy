package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"smart-proxy/internal/guard"
	"smart-proxy/internal/store"
	"smart-proxy/internal/vault"
)

// headers is a fake transport recording the headers the application received.
type headers struct{ got http.Header }

func (h *headers) RoundTrip(r *http.Request) (*http.Response, error) {
	h.got = r.Header.Clone()
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

func protectedHandler(t *testing.T, appUp bool) (*Handler, *headers, string, func() int32) {
	t.Helper()
	route := webRoute("ing-web", "/", "web")
	route.Protection = &store.Protection{Enabled: true, Open: []string{"/webhooks/*", "/health*"}}
	dep := sleeping("web")
	if appUp {
		dep = ready("web", 1)
	}
	h, c, _ := webHandler(t, []*store.RouteConfig{route}, dep)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v := &vault.Vault{Client: fake.NewClientset(), Namespace: "sp", Name: "sp-state"}
	if err := v.Start(ctx); err != nil {
		t.Fatal(err)
	}
	hash, _ := guard.HashPassword("s3cret-pass")
	token := guard.NewToken()
	v.UpdateCredentials(ctx, "ing-web", func(cr *vault.Credentials) {
		cr.Users = map[string]vault.Secret{"team": {Hash: hash}}
		cr.Tokens = map[string]vault.Secret{"jenkins": {Hash: guard.HashToken(token)}}
	})
	h.Guard = &guard.Guard{Vault: v}
	tr := &headers{}
	h.Transport = tr
	return h, tr, token, func() int32 { return replicas(t, c, "web") }
}

func TestStrangersGetTheLoginPageOrA401AndWakeNothing(t *testing.T) {
	h, tr, _, replicasOf := protectedHandler(t, false)

	page := httptest.NewRequest("GET", "http://web.example.com/orders?x=1", nil)
	page.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, page)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/__smart_proxy/login?next=%2Forders%3Fx%3D1" {
		t.Fatalf("browser: %d %q", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://web.example.com/api", nil))
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("script: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://web.example.com/__smart_proxy/status?path=/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status endpoint: %d", w.Code)
	}
	if replicasOf() != 0 || tr.got != nil {
		t.Fatal("a stranger woke the app or reached it")
	}
}

func TestTokensAndOpenPathsGoThrough(t *testing.T) {
	h, tr, token, _ := protectedHandler(t, true)

	r := httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(guard.UserHeader, "admin") // Forged
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || tr.got.Get(guard.UserHeader) != "token:jenkins" || tr.got.Get("Authorization") != "" {
		t.Fatalf("token: %d, app saw user %q, authorization %q", w.Code, tr.got.Get(guard.UserHeader), tr.got.Get("Authorization"))
	}

	r = httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.Header.Set(guard.TokenHeader, token)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if tr.got.Get(guard.TokenHeader) != "" || tr.got.Get(guard.UserHeader) != "token:jenkins" {
		t.Fatal("X-Api-Key not accepted or not removed")
	}

	r = httptest.NewRequest("POST", "http://web.example.com/webhooks/payments", strings.NewReader("{}"))
	r.Header.Set(guard.UserHeader, "admin")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || tr.got.Get(guard.UserHeader) != "" {
		t.Fatalf("open path: %d, forged user %q passed", w.Code, tr.got.Get(guard.UserHeader))
	}

	r = httptest.NewRequest("GET", "http://web.example.com/api", nil)
	r.Header.Set("Authorization", "Bearer spt_wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
}

func TestBrowserLogin(t *testing.T) {
	h, tr, _, _ := protectedHandler(t, true)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://web.example.com/__smart_proxy/login?next=/orders", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "web.example.com") || strings.Contains(w.Body.String(), `name="user"`) {
		t.Fatalf("login page: %d (a single user needs no name)", w.Code)
	}

	login := func(password string) *httptest.ResponseRecorder {
		form := url.Values{"password": {password}, "next": {"/orders"}}
		r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := login("nope-nope"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w = login("s3cret-pass")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/orders" {
		t.Fatalf("login: %d %q", w.Code, w.Header().Get("Location"))
	}
	session := w.Result().Cookies()[0]
	if !session.HttpOnly || session.Name != guard.CookieName("ing-web") {
		t.Fatalf("cookie = %+v", session)
	}

	r := httptest.NewRequest("GET", "http://web.example.com/orders", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(session)
	r.AddCookie(&http.Cookie{Name: "app_session", Value: "keep-me"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || tr.got.Get(guard.UserHeader) != "team" {
		t.Fatalf("signed in: %d, user %q", w.Code, tr.got.Get(guard.UserHeader))
	}
	if c := tr.got.Get("Cookie"); strings.Contains(c, "sp_auth_") || !strings.Contains(c, "app_session=keep-me") {
		t.Fatalf("app received cookies %q", c)
	}
}

func TestTooManyWrongPasswords(t *testing.T) {
	h, _, _, _ := protectedHandler(t, true)
	var last int
	for i := 0; i < 12; i++ {
		form := url.Values{"password": {"wrong-" + time.Now().String()}}
		r := httptest.NewRequest("POST", "http://web.example.com/__smart_proxy/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12 wrong passwords: last answer %d", last)
	}
}
