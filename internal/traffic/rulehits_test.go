package traffic

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMatchRuleSaysWhichRule(t *testing.T) {
	rules := Rules{UserAgents: []string{"Pingdom"}, Paths: []string{"/health*", "/metrics"}, Sources: []string{"10.1.0.0/16"}, Methods: []string{"HEAD"}}
	for _, tc := range []struct {
		path, ua, method, client string
		reason, rule             string
	}{
		{"/healthz", "curl", "GET", "1.2.3.4", ReasonPath, "/health*"},
		{"/metrics", "curl", "GET", "1.2.3.4", ReasonPath, "/metrics"},
		{"/", "Pingdom.com_bot", "GET", "1.2.3.4", ReasonUserAgent, "Pingdom"},
		{"/", "curl", "GET", "10.1.2.3", ReasonSource, "10.1.0.0/16"},
		{"/", "curl", "HEAD", "1.2.3.4", ReasonMethod, "HEAD"},
		{"/", "curl", "GET", "1.2.3.4", "", ""},
	} {
		r := httptest.NewRequest(tc.method, "http://x"+tc.path, nil)
		r.Header.Set("User-Agent", tc.ua)
		reason, rule, ok := rules.MatchRule(r, net.ParseIP(tc.client))
		if reason != tc.reason || rule != tc.rule || ok != (tc.reason != "") {
			t.Errorf("%+v: got %q %q %v", tc, reason, rule, ok)
		}
	}
}

func TestRuleHitsCountAndMerge(t *testing.T) {
	a, b := NewRecorder(), NewRecorder()
	for i := 0; i < 3; i++ {
		a.RecordRule("r1", ReasonPath, "/health*")
	}
	a.RecordRule("r1", "", "ignored: not a rule")
	b.RecordRule("r1", ReasonPath, "/health*")
	b.RecordRule("r1", ReasonUserAgent, "kube-probe")
	b.RecordRule("r2", ReasonPath, "/other")

	merged := MergeRuleHits(a.RuleSnapshot()["r1"], b.RuleSnapshot()["r1"])
	now := time.Now()
	if len(merged) != 2 || merged[0].Rule != "/health*" || merged[0].Total(now) != 4 || merged[1].Total(now) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
	// Hours older than a day don't count.
	old := merged[0]
	old.Hours = map[int64]int64{now.Add(-25*time.Hour).Unix() / 3600: 7}
	if old.Total(now) != 0 {
		t.Fatal("old hour counted")
	}
}

func TestRuleHitsBounded(t *testing.T) {
	r := NewRecorder()
	for i := 0; i < maxRulesPerRoute+10; i++ {
		r.RecordRule("r1", ReasonPath, "/p"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(r.RuleSnapshot()["r1"]); n != maxRulesPerRoute {
		t.Fatalf("%d rules kept", n)
	}
}
