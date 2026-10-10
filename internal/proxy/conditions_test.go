package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"smart-proxy/internal/store"
)

// The beta test: the route serves app-a; app-b, with weight 0, gets only what the beta of the
// mobile app sends, and the clients it got stay there.
func betaRoute(bManaged bool) *store.RouteConfig {
	r := &store.RouteConfig{ID: "ing-portal", Host: "portal.example.com", Path: "/", Namespace: ns, Deployment: "app-a",
		TargetService: "app-a", TargetPort: 8080,
		Backends: []store.WeightedBackend{
			{Service: "app-a", Port: 8080, Weight: 100, Workload: "app-a", Managed: true},
			{Service: "app-b", Port: 8080, Weight: 0, Workload: "app-b", Managed: bManaged, When: []store.Condition{
				{Field: store.FieldHeader, Name: "X-App-Version", Op: store.OpPrefix, Value: "5."},
			}},
		}}
	if err := r.NormalizeBackends(); err != nil {
		panic(err)
	}
	return r
}

func portal(method, path string, header http.Header, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "http://portal.example.com"+path, strings.NewReader("{}"))
	r.Header = header.Clone()
	r.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func TestConditionSendsTheBetaToTheTestBackendAndSticks(t *testing.T) {
	h, c, tr := webHandler(t, []*store.RouteConfig{betaRoute(false)}, ready("app-a", 1), ready("app-b", 1))
	_ = c

	// Ordinary traffic: app-a.
	for i := 0; i < 10; i++ {
		h.ServeHTTP(httptest.NewRecorder(), portal("GET", "/", http.Header{}))
		if tr.last() != "app-a" {
			t.Fatalf("ordinary request went to %q", tr.last())
		}
	}

	// The beta calls: app-b, and the client is pinned there.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, portal("POST", "/api/cart", http.Header{"X-App-Version": {"5.0.0-beta.2"}}))
	if tr.last() != "app-b" {
		t.Fatalf("beta request went to %q", tr.last())
	}
	var pinned *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if strings.HasPrefix(ck.Name, "sp_backend_") {
			pinned = ck
		}
	}
	if pinned == nil || pinned.Value != "app-b" || pinned.SameSite != http.SameSiteNoneMode || !pinned.Secure {
		t.Fatalf("pin cookie = %+v (must be SameSite=None; Secure to survive cross-site posts)", pinned)
	}

	// Its next requests, without the header, stay on app-b.
	h.ServeHTTP(httptest.NewRecorder(), portal("GET", "/home", http.Header{}, pinned))
	if tr.last() != "app-b" {
		t.Fatalf("follow-up went to %q", tr.last())
	}
}

func TestTheLinkPinsAndUnpins(t *testing.T) {
	h, _, tr := webHandler(t, []*store.RouteConfig{betaRoute(false)}, ready("app-a", 1), ready("app-b", 1))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, portal("GET", "/__smart_proxy/use/app-b", http.Header{}))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("link answered %d %q", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Result().Cookies()[0]
	h.ServeHTTP(httptest.NewRecorder(), portal("GET", "/", http.Header{}, cookie))
	if tr.last() != "app-b" {
		t.Fatalf("pinned client went to %q", tr.last())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, portal("GET", "/__smart_proxy/use/default", http.Header{}, cookie))
	if c := w.Result().Cookies()[0]; c.MaxAge >= 0 {
		t.Fatalf("default didn't clear the pin: %+v", c)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, portal("GET", "/__smart_proxy/use/nope", http.Header{}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown backend: %d", w.Code)
	}
}

func TestConditionalBackendWakesOnlyWhenUsed(t *testing.T) {
	h, c, tr := webHandler(t, []*store.RouteConfig{betaRoute(true)}, ready("app-a", 1), sleeping("app-b"))

	h.ServeHTTP(httptest.NewRecorder(), portal("GET", "/", http.Header{}))
	if tr.last() != "app-a" || replicas(t, c, "app-b") != 0 {
		t.Fatalf("ordinary traffic woke app-b (went to %q)", tr.last())
	}

	// The beta's request wakes app-b (not app-a's chain), waits for it, then reaches it.
	h.ServeHTTP(httptest.NewRecorder(), portal("POST", "/api/cart", http.Header{"X-App-Version": {"5.0.0-beta.2"}}))
	if replicas(t, c, "app-b") == 0 {
		t.Fatal("app-b not woken by the request sent to it")
	}
}

func TestUnmanagedConditionalBackendDownIsReported(t *testing.T) {
	h, _, tr := webHandler(t, []*store.RouteConfig{betaRoute(false)}, ready("app-a", 1), sleeping("app-b"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, portal("POST", "/api/cart", http.Header{"X-App-Version": {"5.0.0-beta.2"}}))
	if w.Code != http.StatusServiceUnavailable || tr.last() != "" || !strings.Contains(w.Body.String(), "app-b") {
		t.Fatalf("got %d %q via %q: must not silently fall back to app-a", w.Code, w.Body.String(), tr.last())
	}
}

func TestMatchCondition(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/api/v2/orders?variant=b", nil)
	r.Header.Set("X-Tenant", "Acme Corp")
	r.AddCookie(&http.Cookie{Name: "beta", Value: "yes"})
	client := net.ParseIP("10.20.30.40")
	cases := []struct {
		c    store.Condition
		want bool
	}{
		{store.Condition{Field: "header", Name: "x-tenant", Op: "equals", Value: "acme corp"}, true},
		{store.Condition{Field: "header", Name: "X-Tenant", Op: "contains", Value: "acme"}, true},
		{store.Condition{Field: "header", Name: "X-Missing", Op: "exists"}, false},
		{store.Condition{Field: "cookie", Name: "beta", Op: "equals", Value: "yes"}, true},
		{store.Condition{Field: "query", Name: "variant", Op: "equals", Value: "b"}, true},
		{store.Condition{Field: "query", Name: "variant", Op: "equals", Value: "a"}, false},
		{store.Condition{Field: "path", Op: "prefix", Value: "/api/v2"}, true},
		{store.Condition{Field: "client", Op: "equals", Value: "10.20.0.0/16"}, true},
		{store.Condition{Field: "client", Op: "equals", Value: "192.168.0.1"}, false},
	}
	for _, tc := range cases {
		if got := matchCondition(tc.c, r, client); got != tc.want {
			t.Errorf("%+v = %v, want %v", tc.c, got, tc.want)
		}
	}
}
