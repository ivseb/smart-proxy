// Package traffic decides which requests count as user activity, and records who sends
// requests to each route. Uptime monitors and health checks poll applications around the clock;
// counted as activity they would keep environments awake forever (and wake them right back).
package traffic

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// DefaultUserAgents are monitoring tools and health checkers, matched case-insensitively as
// substrings of the User-Agent header.
var DefaultUserAgents = []string{
	"UptimeRobot", "Pingdom", "StatusCake", "Site24x7", "Datadog", "Better Uptime", "BetterStack",
	"Uptime-Kuma", "Uptime Kuma", "Blackbox Exporter", "kube-probe", "GoogleHC", "GoogleStackdriverMonitoring",
	"ELB-HealthChecker", "Amazon-Route53-Health-Check", "Zabbix", "Nagios", "check_http", "Checkly",
	"NewRelicPinger", "Freshping", "HetrixTools", "Uptime.com", "Oh Dear", "Cloudflare-Healthchecks",
	"Grafana Synthetic Monitoring", "SRE Synthetic", "Dynatrace Synthetic", "Prometheus",
}

// DefaultTrustedProxies are the networks whose X-Forwarded-For is believed: in-cluster ingress
// controllers and routers connect from private addresses.
var DefaultTrustedProxies = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "100.64.0.0/10", "fc00::/7", "::1/128"}

// Rules select requests that must not count as activity.
type Rules struct {
	// UserAgents are case-insensitive substrings of the User-Agent header.
	UserAgents []string `json:"user_agents,omitempty"`
	// Paths are exact paths, or prefixes ending with "*" (e.g. "/health*").
	Paths []string `json:"paths,omitempty"`
	// Sources are client IPs or CIDRs.
	Sources []string `json:"sources,omitempty"`
	// Methods are HTTP methods, e.g. HEAD.
	Methods []string `json:"methods,omitempty"`
}

// IsZero reports whether no rule is set.
func (r Rules) IsZero() bool {
	return len(r.UserAgents)+len(r.Paths)+len(r.Sources)+len(r.Methods) == 0
}

// Validate reports malformed rules.
func (r Rules) Validate() error {
	for _, p := range r.Paths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("path %q must start with /", p)
		}
	}
	for _, s := range r.Sources {
		if _, err := parseNet(s); err != nil {
			return err
		}
	}
	for _, m := range r.Methods {
		if m == "" || strings.ContainsAny(m, " /") {
			return fmt.Errorf("invalid method %q", m)
		}
	}
	return nil
}

// Merge returns the union of two rule sets.
func (r Rules) Merge(other Rules) Rules {
	return Rules{
		UserAgents: append(append([]string{}, r.UserAgents...), other.UserAgents...),
		Paths:      append(append([]string{}, r.Paths...), other.Paths...),
		Sources:    append(append([]string{}, r.Sources...), other.Sources...),
		Methods:    append(append([]string{}, r.Methods...), other.Methods...),
	}
}

// Reasons a request is ignored, as reported in metrics and the dashboard.
const (
	ReasonUserAgent = "user-agent"
	ReasonPath      = "path"
	ReasonSource    = "source"
	ReasonMethod    = "method"
	ReasonProbePath = "probe-path" // The path of one of the workload's Kubernetes probes
)

// Match reports whether a request matches the rules, and which rule matched.
func (r Rules) Match(req *http.Request, client net.IP) (string, bool) {
	if ua := strings.ToLower(req.UserAgent()); ua != "" {
		for _, pattern := range r.UserAgents {
			if p := strings.ToLower(strings.TrimSpace(pattern)); p != "" && strings.Contains(ua, p) {
				return ReasonUserAgent, true
			}
		}
	}
	for _, pattern := range r.Paths {
		if MatchPath(pattern, req.URL.Path) {
			return ReasonPath, true
		}
	}
	if client != nil {
		for _, s := range r.Sources {
			if n, err := parseNet(s); err == nil && n.Contains(client) {
				return ReasonSource, true
			}
		}
	}
	for _, m := range r.Methods {
		if strings.EqualFold(m, req.Method) {
			return ReasonMethod, true
		}
	}
	return "", false
}

// MatchPath matches an exact path, or a prefix when the pattern ends with "*".
func MatchPath(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(path, prefix)
	}
	return pattern != "" && path == pattern
}

func parseNet(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q", s)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("invalid IP or CIDR %q", s)
	}
	return n, nil
}

// TrustedProxies decides when X-Forwarded-For is believed.
type TrustedProxies []*net.IPNet

// ParseTrustedProxies parses a list of CIDRs/IPs.
func ParseTrustedProxies(list []string) (TrustedProxies, error) {
	var t TrustedProxies
	for _, s := range list {
		if strings.TrimSpace(s) == "" {
			continue
		}
		n, err := parseNet(s)
		if err != nil {
			return nil, err
		}
		t = append(t, n)
	}
	return t, nil
}

func (t TrustedProxies) trusts(ip net.IP) bool {
	for _, n := range t {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address of the client: the connection's peer, or, when the peer is a
// trusted proxy, the right-most untrusted address in X-Forwarded-For (anything to its left can
// be forged by the client).
func (t TrustedProxies) ClientIP(req *http.Request) net.IP {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !t.trusts(ip) {
		return ip
	}
	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := net.ParseIP(strings.TrimSpace(hops[i]))
		if hop == nil {
			continue
		}
		if !t.trusts(hop) {
			return hop
		}
		ip = hop
	}
	return ip
}

// SplitList splits comma- or newline-separated values, trimming blanks.
func SplitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// NearestClient is the address the nearest trusted proxy saw the request come from (the last
// X-Forwarded-For entry it appended), or the peer itself. Unlike ClientIP it can't be chosen by
// the client when clients are on trusted (private) networks too: what rate limits key on.
func (t TrustedProxies) NearestClient(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !t.trusts(ip) {
		return host
	}
	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	if last := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); last != nil {
		return last.String()
	}
	return host
}
