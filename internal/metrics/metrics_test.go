package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsExposition(t *testing.T) {
	Request("team-a", "ing-team-a/web")
	WakeStarted("team-a", "web", "request")
	Ready("team-a", "web")
	Ready("team-a", "web") // No pending wake: not observed twice
	Slept("team-a", "web", "idle")
	SetLeader(true)
	RegisterSleeping(func() (map[string]int, map[string]int) {
		return SleepingFromAnnotations([]string{"team-a", "team-a"}, []string{"3", "bogus"})
	})

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`smart_proxy_requests_total{namespace="team-a",route="ing-team-a/web"} 1`,
		`smart_proxy_wakeups_total{deployment="web",namespace="team-a",trigger="request"} 1`,
		`smart_proxy_wake_duration_seconds_count{deployment="web",namespace="team-a"} 1`,
		`smart_proxy_sleeps_total{deployment="web",namespace="team-a",reason="idle"} 1`,
		`smart_proxy_leader 1`,
		`smart_proxy_sleeping_deployments{namespace="team-a"} 2`,
		`smart_proxy_sleeping_replicas{namespace="team-a"} 4`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q", want)
		}
	}
}
