package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

func pauseRoute(t *testing.T, h *Handler, id string, until *time.Time) {
	t.Helper()
	r, _ := h.store.GetRoute(id)
	r.Pause = &store.Pause{At: time.Now(), Until: until}
	if err := h.store.AddRoute(r); err != nil {
		t.Fatal(err)
	}
}

// Paused, a route only forwards: a monitor reaching it while asleep wakes it like anyone.
func TestPausedRouteDoesntAnswerForTheApp(t *testing.T) {
	h, c, id := monitorHandler(t, "")
	pauseRoute(t, h, id, nil)
	w := get(h, "/healthz", "curl/8")
	if w.Header().Get("X-Smart-Proxy") == "asleep" || replicas(t, c, "web") == 0 {
		t.Fatalf("paused route answered for the app (%d) or didn't wake it", w.Code)
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
