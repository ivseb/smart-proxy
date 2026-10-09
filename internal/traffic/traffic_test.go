package traffic

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRulesMatch(t *testing.T) {
	rules := Rules{UserAgents: DefaultUserAgents, Paths: []string{"/healthz", "/status/*"}, Sources: []string{"10.1.2.0/24"}, Methods: []string{"HEAD"}}
	cases := []struct {
		method, path, ua, ip string
		want                 string
	}{
		{"GET", "/", "Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)", "1.2.3.4", ReasonUserAgent},
		{"GET", "/healthz", "curl/8", "1.2.3.4", ReasonPath},
		{"GET", "/healthz/deep", "curl/8", "1.2.3.4", ""},
		{"GET", "/status/db", "curl/8", "1.2.3.4", ReasonPath},
		{"GET", "/", "curl/8", "10.1.2.9", ReasonSource},
		{"HEAD", "/", "curl/8", "1.2.3.4", ReasonMethod},
		{"GET", "/shop", "Mozilla/5.0 (Macintosh) AppleWebKit/605 Version/17 Safari/605", "1.2.3.4", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("User-Agent", tc.ua)
		reason, ok := rules.Match(r, net.ParseIP(tc.ip))
		if reason != tc.want || ok != (tc.want != "") {
			t.Errorf("%s %s %q from %s: got %q %v, want %q", tc.method, tc.path, tc.ua, tc.ip, reason, ok, tc.want)
		}
	}
	if err := (Rules{Paths: []string{"health"}}).Validate(); err == nil {
		t.Error("relative path accepted")
	}
	if err := (Rules{Sources: []string{"10.0.0.0/33"}}).Validate(); err == nil {
		t.Error("bad CIDR accepted")
	}
}

func TestClientIP(t *testing.T) {
	trusted, _ := ParseTrustedProxies(DefaultTrustedProxies)
	cases := []struct {
		remote string
		xff    string
		want   string
	}{
		{"203.0.113.7:4000", "198.51.100.1", "203.0.113.7"},                  // Untrusted peer: XFF ignored
		{"10.0.0.5:4000", "198.51.100.1", "198.51.100.1"},                    // Via the ingress controller
		{"10.0.0.5:4000", "6.6.6.6, 198.51.100.1, 10.0.0.9", "198.51.100.1"}, // Right-most untrusted
		{"10.0.0.5:4000", "", "10.0.0.5"},                                    // In-cluster client
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := trusted.ClientIP(r).String(); got != tc.want {
			t.Errorf("%s / %q: got %s, want %s", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestClientName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)":                          "UptimeRobot",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML) Chrome/130.0 Safari/537.36": Browser,
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)":                        "Googlebot",
		"curl/8.4.0":         "curl",
		"Go-http-client/1.1": "Go-http-client",
		"kube-probe/1.30":    "kube-probe",
		"":                   "(no user agent)",
	} {
		if got := ClientName(ua); got != want {
			t.Errorf("ClientName(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestRecorderAndMerge(t *testing.T) {
	rec := NewRecorder()
	monitor := httptest.NewRequest("GET", "/", nil)
	monitor.Header.Set("User-Agent", "UptimeRobot/2.0")
	for i := 0; i < 3; i++ {
		rec.Record("r1", monitor, net.ParseIP("1.2.3.4"), ReasonUserAgent)
	}
	for _, p := range []string{"/a", "/b", "/c"} {
		b := httptest.NewRequest("GET", p, nil)
		b.Header.Set("User-Agent", "Mozilla/5.0 (X11) Firefox/131.0")
		rec.Record("r1", b, net.ParseIP("5.6.7.8"), "")
	}
	// Once ignored (e.g. a rule was just added), a client no longer counts.
	late := httptest.NewRequest("GET", "/", nil)
	late.Header.Set("User-Agent", "MyPinger/1.0")
	rec.Record("r2", late, nil, "")
	rec.Record("r2", late, nil, ReasonUserAgent)
	if s := rec.Snapshot()["r2"][0]; !s.LastIgnored || s.Ignored != 1 || s.Requests != 2 {
		t.Fatalf("r2 = %+v", s)
	}

	got := rec.Snapshot()["r1"]
	if len(got) != 2 {
		t.Fatalf("sources = %+v (browsers should be one entry)", got)
	}

	other := []SourceStats{{Client: "UptimeRobot", Method: "GET", Path: "/", Requests: 2, Ignored: 2, FirstSeen: time.Now().Add(-time.Hour), LastSeen: time.Now()}}
	merged := Merge(got, other)
	if merged[0].Client != "UptimeRobot" || merged[0].Requests != 5 || merged[0].Ignored != 5 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged[0].Interval() <= 0 {
		t.Fatal("interval not computed")
	}
}

func TestRecorderIsBounded(t *testing.T) {
	rec := NewRecorder()
	for i := 0; i < 50; i++ {
		r := httptest.NewRequest("GET", "/p"+string(rune('a'+i%26))+string(rune('a'+i/26)), nil)
		r.Header.Set("User-Agent", "curl/8")
		rec.Record("r1", r, nil, "")
	}
	if n := len(rec.Snapshot()["r1"]); n > maxSourcesPerRoute {
		t.Fatalf("%d sources kept, max %d", n, maxSourcesPerRoute)
	}
}
