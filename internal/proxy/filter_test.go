package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

func monitorHandler(t *testing.T, whenAsleep string) (*Handler, *fakecluster.Cluster, string) {
	t.Helper()
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, fakecluster.Options{}, []runtime.Object{sleeping("web")}...)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	id := store.IngressID(ns, "web")
	st.AddRoute(&store.RouteConfig{ID: id, Host: "web.example.com", Path: "/", Namespace: ns, Deployment: "web",
		TargetService: "web-svc", TargetPort: 8080, WhenAsleep: whenAsleep,
		Ignore: &traffic.Rules{Paths: []string{"/healthz"}}})
	st.SetActivityForTest(id, time.Now().Add(-time.Hour))
	h := &Handler{k8sClient: c.Client, store: st, Metrics: NewMetrics(), Traffic: traffic.NewRecorder(),
		GlobalRules: traffic.Rules{UserAgents: traffic.DefaultUserAgents}, WakeTimeout: time.Second}
	h.SetReady()
	return h, c, id
}

func get(h *Handler, path, ua string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://web.example.com"+path, nil)
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Accept", "text/html,*/*")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func lastActivity(h *Handler, id string) time.Time {
	r, _ := h.store.GetRoute(id)
	return r.LastActivity
}

func TestMonitorsDontWakeSleepingRoutes(t *testing.T) {
	h, c, id := monitorHandler(t, "")
	before := lastActivity(h, id)

	w := get(h, "/", "Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)")
	if w.Code != http.StatusOK || w.Header().Get("X-Smart-Proxy") != "asleep" {
		t.Fatalf("monitor got %d %v", w.Code, w.Header())
	}
	w = get(h, "/healthz", "curl/8") // Route-specific rule
	if w.Code != http.StatusOK || w.Header().Get("X-Smart-Proxy") != "asleep" {
		t.Fatalf("health check got %d", w.Code)
	}
	if replicas(t, c, "web") != 0 {
		t.Fatal("a monitor woke the deployment")
	}
	if !lastActivity(h, id).Equal(before) {
		t.Fatal("a monitor counted as activity")
	}

	sources := traffic.Merge(h.Traffic.Snapshot()[id])
	if len(sources) != 2 || sources[0].Ignored != 1 || sources[1].Ignored != 1 {
		t.Fatalf("sources = %+v", sources)
	}

	// A visitor wakes it.
	w = get(h, "/", "Mozilla/5.0 (Macintosh) AppleWebKit/605 Version/17 Safari/605")
	if w.Header().Get("X-Smart-Proxy") == "asleep" {
		t.Fatal("visitor got the asleep answer")
	}
	if replicas(t, c, "web") == 0 {
		t.Fatal("visitor didn't wake the deployment")
	}
	if !lastActivity(h, id).After(before) {
		t.Fatal("visitor didn't count as activity")
	}
}

func TestMonitorsCanGetUnavailableOrWake(t *testing.T) {
	h, c, _ := monitorHandler(t, store.WhenAsleepUnavailable)
	if w := get(h, "/", "UptimeRobot/2.0"); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("unavailable: got %d", w.Code)
	}
	if replicas(t, c, "web") != 0 {
		t.Fatal("woken")
	}

	h, c, id := monitorHandler(t, store.WhenAsleepWake)
	before := lastActivity(h, id)
	get(h, "/", "UptimeRobot/2.0")
	if replicas(t, c, "web") == 0 {
		t.Fatal("wake mode didn't wake")
	}
	if !lastActivity(h, id).Equal(before) {
		t.Fatal("wake mode counted as activity")
	}
}
