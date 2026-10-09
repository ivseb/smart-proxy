package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

func webHandler(t *testing.T, routes []*store.RouteConfig, objs ...runtime.Object) (*Handler, *fakecluster.Cluster, *recorder) {
	t.Helper()
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, fakecluster.Options{}, objs...)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	for _, r := range routes {
		st.AddRoute(r)
	}
	tr := &recorder{}
	h := &Handler{k8sClient: c.Client, store: st, Metrics: NewMetrics(), Transport: tr, WakeTimeout: time.Second}
	h.SetReady()
	return h, c, tr
}

func webRoute(id, path, deployment string) *store.RouteConfig {
	return &store.RouteConfig{ID: id, Host: "web.example.com", Path: path, Namespace: ns, Deployment: deployment,
		TargetService: deployment, TargetPort: 8080}
}

// API calls, form posts and WebSockets are not answered with the HTML "waking up" page.
func TestNonPageRequestsWaitForTheApp(t *testing.T) {
	h, c, tr := webHandler(t, []*store.RouteConfig{webRoute("ing-web", "/", "web")}, sleeping("web"))

	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://web.example.com/api/orders", strings.NewReader(`{"id":1}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || tr.last() != "" {
		t.Fatalf("POST to a sleeping app: %d %q, proxied to %q", w.Code, w.Header().Get("Content-Type"), tr.last())
	}
	if replicas(t, c, "web") == 0 {
		t.Fatal("POST didn't wake the app")
	}

	// Once it is ready the request goes through, body included.
	markReady(t, c, "web")
	if w := post(); w.Code != http.StatusOK || tr.last() != "web" {
		t.Fatalf("POST after wake: %d via %q", w.Code, tr.last())
	}
}

func TestRootProbePathCountsAsActivity(t *testing.T) {
	dep := sleeping("web")
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "web", ReadinessProbe: &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/"}}}}}
	h, c, _ := webHandler(t, []*store.RouteConfig{webRoute("ing-web", "/", "web")}, dep)
	fakecluster.Eventually(t, func() bool {
		paths, _ := c.GetDeploymentProbePaths(ns, "web")
		return len(paths) == 1
	}, "probe paths not cached")

	r := httptest.NewRequest("GET", "http://web.example.com/", nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if replicas(t, c, "web") == 0 {
		t.Fatal("the home page didn't wake the app because \"/\" is its probe path")
	}
}

func TestPathsMatchWholeSegmentsAndHostsTrailingDots(t *testing.T) {
	h, _, _ := webHandler(t, []*store.RouteConfig{webRoute("ing-api", "/api", "api"), webRoute("ing-web", "/", "web"),
		{ID: "route-ns/legacy", Host: "web.example.com", Path: "/old", Namespace: ns, Deployment: "legacy"}})
	cases := map[string]string{
		"/api": "ing-api", "/api/x": "ing-api", "/apidocs": "ing-web", "/": "ing-web",
		"/oldies": "route-ns/legacy", // OpenShift Routes match plain prefixes, like the router
	}
	for path, want := range cases {
		if got, _ := h.matchRoute("web.example.com", path); got.ID != want {
			t.Errorf("%s -> %s, want %s", path, got.ID, want)
		}
	}
	for raw, want := range map[string]string{"/api/../old": "/old", "//api//x/": "/api/x/", "/./": "/", "": "/"} {
		if got := cleanPath(raw); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", raw, got, want)
		}
	}
	r := httptest.NewRequest("GET", "http://web.example.com./", nil)
	if requestHost(r) != "web.example.com" {
		t.Errorf("requestHost = %q", requestHost(r))
	}
}

func TestStatusCheckIgnoresOtherHosts(t *testing.T) {
	h, c, _ := webHandler(t, []*store.RouteConfig{webRoute("ing-web", "/", "web"),
		{ID: "ing-internal", Host: "internal.example.com", Path: "/", Namespace: ns, Deployment: "internal"}},
		ready("web", 1), sleeping("internal"))
	r := httptest.NewRequest("GET", "http://web.example.com/__smart_proxy/status?host=internal.example.com", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if replicas(t, c, "internal") != 0 {
		t.Fatal("the status check woke a route of another host")
	}
}

func TestBadgeSkipsResponsesWithoutBody(t *testing.T) {
	for _, status := range []int{http.StatusNotModified, http.StatusNoContent} {
		resp := &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(""))}
		injectBadge(resp)
		if resp.Header.Get("Content-Length") != "" {
			t.Errorf("badge added to a %d", status)
		}
	}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<body>hi</body>"))}
	injectBadge(resp)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Powered by") || !strings.HasSuffix(string(body), "</body>") {
		t.Errorf("page = %s", body)
	}
}

func TestHostDecidesBeforePath(t *testing.T) {
	h, _, _ := webHandler(t, []*store.RouteConfig{
		webRoute("ing-web", "/", "web"),
		{ID: "ing-other/api", Host: "", Path: "/api", Namespace: "other", Deployment: "api"},
		{ID: "ing-wild", Host: "*.preview.example.com", Path: "/", Namespace: ns, Deployment: "preview"},
		{ID: "ing-slash", Host: "s.example.com", Path: "/api/", Namespace: ns, Deployment: "api"},
	})
	cases := []struct{ host, path, want string }{
		{"web.example.com", "/api/orders", "ing-web"}, // Not the catch-all of another app
		{"Web.Example.com", "/", "ing-web"},           // Hosts are case-insensitive
		{"x.example.org", "/api/x", "ing-other/api"},  // No route for the host: the catch-all
		{"pr-1.preview.example.com", "/", "ing-wild"}, // Wildcards cover one label
		{"a.b.preview.example.com", "/", ""},          // ... not two
		{"s.example.com", "/api", "ing-slash"},        // Prefix "/api/" serves "/api"
	}
	for _, c := range cases {
		got, found := h.matchRoute(c.host, c.path)
		if (c.want == "" && found) || (c.want != "" && got.ID != c.want) {
			t.Errorf("%s%s -> %q (found %v), want %q", c.host, c.path, got.ID, found, c.want)
		}
	}

	// The index follows configuration changes.
	h.store.AddRoute(&store.RouteConfig{ID: "ing-new", Host: "new.example.com", Path: "/", Namespace: ns, Deployment: "new"})
	if got, _ := h.matchRoute("new.example.com", "/"); got.ID != "ing-new" {
		t.Errorf("new route not matched: %q", got.ID)
	}
}

// An app that can't be woken (e.g. an admission webhook refuses the scale-up) is not proxied to
// as if it served, and failing requests don't each ask the API server again.
func TestRefusedWakeIsNotProxied(t *testing.T) {
	h, c, tr := webHandler(t, []*store.RouteConfig{webRoute("ing-web", "/", "web")}, sleeping("web"))
	patches := 0
	c.Kube.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		patches++
		return true, nil, fmt.Errorf("denied by webhook")
	})
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", "http://web.example.com/api", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable || tr.last() != "" {
			t.Fatalf("request %d: %d, proxied to %q", i, w.Code, tr.last())
		}
	}
	if patches > 2 {
		t.Fatalf("%d scale-up attempts for 5 requests", patches)
	}
}

func TestBadgeLeavesPartialContentAlone(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Type": {"text/html"}, "Content-Range": {"bytes 0-9/100"}},
		Body: io.NopCloser(strings.NewReader("<body>hi</"))}
	injectBadge(resp)
	if body, _ := io.ReadAll(resp.Body); string(body) != "<body>hi</" {
		t.Fatalf("partial content changed: %q", body)
	}
}
