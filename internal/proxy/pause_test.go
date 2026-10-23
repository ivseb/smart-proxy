package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pauseRoute(t *testing.T, h *Handler, id string, until *time.Time) {
	t.Helper()
	r, _ := h.store.GetRoute(id)
	r.Pause = &store.Pause{At: time.Now(), Until: until}
	if err := h.store.AddRoute(r); err != nil {
		t.Fatal(err)
	}
}

// Paused, a route is left as it is: asleep, nothing wakes it (503, the dashboard's Wake now
// starts it); running, it is a plain reverse proxy.
func TestPausedRouteLeavesTheAppAsItIs(t *testing.T) {
	h, c, id := monitorHandler(t, "")
	pauseRoute(t, h, id, nil)
	for _, ua := range []string{"curl/8", "Mozilla/5.0"} { // A monitor, a browser
		w := get(h, "/healthz", ua)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Smart-Proxy") != "paused" || replicas(t, c, "web") != 0 {
			t.Fatalf("%s: %d %v, replicas %d", ua, w.Code, w.Header(), replicas(t, c, "web"))
		}
	}
	// Woken from the dashboard: forwarded.
	if _, err := h.k8sClient.WakeDeployment(ns, "web"); err != nil {
		t.Fatal(err)
	}
	fakecluster.Eventually(t, func() bool { r, _, _ := c.GetDeploymentStatus(ns, "web"); return r > 0 }, "wake not in cache")
	if w := get(h, "/", "Mozilla/5.0"); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("starting: %d %v", w.Code, w.Header())
	}
	markReady(t, c, "web")
	if w := get(h, "/", "Mozilla/5.0"); w.Header().Get("X-Smart-Proxy") == "paused" {
		t.Fatalf("running and paused: %d %v", w.Code, w.Header())
	}
}

// Paused, a route with backends sends everything to its main one: no conditions, no pins.
func TestPausedRouteIgnoresOtherBackends(t *testing.T) {
	h, _, tr := webHandler(t, []*store.RouteConfig{betaRoute(false)}, ready("app-a", 1), ready("app-b", 1))
	pauseRoute(t, h, "ing-portal", nil)
	h.ServeHTTP(httptest.NewRecorder(), portal("POST", "/api/cart", http.Header{"X-App-Version": {"5.0.0-beta.2"}}))
	if tr.last() != "app-a" {
		t.Fatalf("paused route sent the beta to %q", tr.last())
	}
	cookie := &http.Cookie{Name: stickyCookie("ing-portal"), Value: "app-b"}
	h.ServeHTTP(httptest.NewRecorder(), portal("GET", "/", http.Header{}, cookie))
	if tr.last() != "app-a" {
		t.Fatalf("paused route followed the pin to %q", tr.last())
	}

	// Expired: conditions apply again.
	past := time.Now().Add(-time.Minute)
	pauseRoute(t, h, "ing-portal", &past)
	h.ServeHTTP(httptest.NewRecorder(), portal("POST", "/api/cart", http.Header{"X-App-Version": {"5.0.0-beta.2"}}))
	if tr.last() != "app-b" {
		t.Fatalf("after the pause the beta went to %q", tr.last())
	}
}

// Ignore rules count what they match, by rule.
func TestIgnoredRequestsAreCountedByRule(t *testing.T) {
	h, _, id := monitorHandler(t, "")
	get(h, "/healthz", "curl/8")
	get(h, "/healthz", "curl/8")
	get(h, "/", "Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)")
	hits := traffic.MergeRuleHits(h.Traffic.RuleSnapshot()[id])
	now := time.Now()
	if len(hits) != 2 || hits[0].Rule != "/healthz" || hits[0].Total(now) != 2 || hits[1].Rule != "UptimeRobot" {
		t.Fatalf("hits = %+v", hits)
	}
}

// The "waking up" page left open polls the status every second: paused, that doesn't start
// the application again (scaled down by hand, it stays down) and the page reloads into the 503.
func TestPausedRouteStatusDoesntWake(t *testing.T) {
	h, c, id := monitorHandler(t, "")
	status := func() string {
		t.Helper()
		w := get(h, "/__smart_proxy/status?path=%2F", "Mozilla/5.0")
		var res map[string]any
		if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
			t.Fatalf("%d: %v", w.Code, err)
		}
		return res["status"].(string)
	}
	if status() != "waiting" { // Not paused: the page wakes it
		t.Fatal("not woken")
	}
	waitScaled(t, c, "web")

	pauseRoute(t, h, id, nil)
	setReady(t, c, "web", 0) // Scaled down by hand, from the console
	fakecluster.Eventually(t, func() bool { r, _, _ := c.GetDeploymentStatus(ns, "web"); return r == 0 }, "scale-down not in cache")
	for range 3 {
		if s := status(); s != "paused" || replicas(t, c, "web") != 0 {
			t.Fatalf("paused: %q, replicas %d", s, replicas(t, c, "web"))
		}
	}

	// Started again (from the dashboard, or by hand): the page follows it.
	d, _ := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), "web", metav1.GetOptions{})
	one := int32(1)
	d.Spec.Replicas = &one
	if _, err := c.Kube.AppsV1().Deployments(ns).Update(context.TODO(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitScaled(t, c, "web")
	if s := status(); s != "waiting" {
		t.Fatalf("starting: %q", s)
	}
	markReady(t, c, "web")
	if s := status(); s != "ready" {
		t.Fatalf("running: %q", s)
	}
}
