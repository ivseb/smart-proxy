package traffic

import (
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Browser groups all human traffic of a route into one entry.
const Browser = "Browser"

// SourceStats summarizes requests to a route from one kind of client.
type SourceStats struct {
	// Client is a monitor/tool name (e.g. "UptimeRobot", "curl") or "Browser".
	Client string `json:"client"`
	// Method and Path are those of its requests ("*" for browsers, whose paths vary).
	Method string `json:"method"`
	Path   string `json:"path"`
	// UserAgent is the latest full User-Agent seen (truncated).
	UserAgent string `json:"user_agent"`
	LastIP    string `json:"last_ip"`
	Requests  int64  `json:"requests"`
	Ignored   int64  `json:"ignored"`
	Reason    string `json:"reason,omitempty"` // Why the latest ignored request was ignored
	// LastIgnored is true when the latest request was ignored: the client no longer counts.
	LastIgnored bool      `json:"last_ignored"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// Interval is the average time between requests (0 when unknown).
func (s SourceStats) Interval() time.Duration {
	if s.Requests < 2 {
		return 0
	}
	return s.LastSeen.Sub(s.FirstSeen) / time.Duration(s.Requests-1)
}

func (s SourceStats) key() string { return s.Client + " " + s.Method + " " + s.Path }

const (
	maxSourcesPerRoute = 12
	maxUserAgentLength = 160
	forgetAfter        = 24 * time.Hour
)

// Recorder keeps, per route, the clients sending it requests (bounded, forgetting clients not
// seen for a day).
type Recorder struct {
	mu     sync.Mutex
	routes map[string]map[string]*SourceStats
}

func NewRecorder() *Recorder {
	return &Recorder{routes: map[string]map[string]*SourceStats{}}
}

// Record notes a request to a route.
func (r *Recorder) Record(routeID string, req *http.Request, client net.IP, ignoredReason string) {
	ua := req.UserAgent()
	stats := SourceStats{Client: ClientName(ua), Method: req.Method, Path: req.URL.Path}
	if stats.Client == Browser {
		stats.Method, stats.Path = "*", "*"
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	sources := r.routes[routeID]
	if sources == nil {
		sources = map[string]*SourceStats{}
		r.routes[routeID] = sources
	}
	entry, ok := sources[stats.key()]
	if !ok {
		if len(sources) >= maxSourcesPerRoute {
			evict(sources, now)
		}
		stats.FirstSeen = now
		entry = &stats
		sources[stats.key()] = entry
	}
	entry.Requests++
	entry.LastSeen = now
	entry.UserAgent = truncate(ua, maxUserAgentLength)
	if client != nil {
		entry.LastIP = client.String()
	}
	entry.LastIgnored = ignoredReason != ""
	if ignoredReason != "" {
		entry.Ignored++
		entry.Reason = ignoredReason
	}
}

// evict drops clients not seen for a day, or else the one with the fewest requests
// (periodic monitors accumulate requests and stay).
func evict(sources map[string]*SourceStats, now time.Time) {
	var weakest string
	for k, s := range sources {
		if now.Sub(s.LastSeen) > forgetAfter {
			delete(sources, k)
			return
		}
		if weakest == "" || s.Requests < sources[weakest].Requests {
			weakest = k
		}
	}
	delete(sources, weakest)
}

// Snapshot returns every route's clients.
func (r *Recorder) Snapshot() map[string][]SourceStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]SourceStats, len(r.routes))
	now := time.Now()
	for id, sources := range r.routes {
		for k, s := range sources {
			if now.Sub(s.LastSeen) > forgetAfter {
				delete(sources, k)
				continue
			}
			out[id] = append(out[id], *s)
		}
	}
	return out
}

// Merge combines the clients seen by several replicas, most requests first.
func Merge(lists ...[]SourceStats) []SourceStats {
	merged := map[string]*SourceStats{}
	for _, list := range lists {
		for _, s := range list {
			m, ok := merged[s.key()]
			if !ok {
				c := s
				merged[s.key()] = &c
				continue
			}
			m.Requests += s.Requests
			m.Ignored += s.Ignored
			if s.FirstSeen.Before(m.FirstSeen) {
				m.FirstSeen = s.FirstSeen
			}
			if s.LastSeen.After(m.LastSeen) {
				m.LastSeen, m.UserAgent, m.LastIP, m.LastIgnored = s.LastSeen, s.UserAgent, s.LastIP, s.LastIgnored
				if s.Reason != "" {
					m.Reason = s.Reason
				}
			}
		}
	}
	out := make([]SourceStats, 0, len(merged))
	for _, s := range merged {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].key() < out[j].key()
	})
	return out
}

var (
	browserRE = regexp.MustCompile(`(?i)\b(chrome|chromium|firefox|safari|edg|opr|crios|fxios)/`)
	botRE     = regexp.MustCompile(`(?i)(bot|crawler|spider|monitor|check|probe|uptime|synthetic|headless)`)
	tokenRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+-]*`)
)

// ClientName names the client behind a User-Agent: a known monitor, "Browser", or the first
// product token ("curl", "Go-http-client", "python-requests"…).
func ClientName(ua string) string {
	if ua == "" {
		return "(no user agent)"
	}
	lower := strings.ToLower(ua)
	for _, m := range DefaultUserAgents {
		if strings.Contains(lower, strings.ToLower(m)) {
			return m
		}
	}
	if browserRE.MatchString(ua) && !botRE.MatchString(ua) {
		return Browser
	}
	// "Mozilla/5.0 (compatible; Foo/1.0; +http://…)": the real client is inside.
	if i := strings.Index(lower, "compatible;"); i >= 0 {
		if name := tokenRE.FindString(strings.TrimSpace(ua[i+len("compatible;"):])); name != "" {
			return name
		}
	}
	if name := tokenRE.FindString(ua); name != "" {
		return name
	}
	return truncate(ua, 40)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
