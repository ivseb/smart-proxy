// Package inspect records the requests a route receives, while its recording is on, so the
// dashboard can show what arrives (to write ignore rules or backend conditions, or to debug).
// Each replica keeps the latest requests it served in memory; bodies are never recorded and
// credentials are masked.
package inspect

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Outcomes of a request, as shown in the dashboard.
const (
	OutcomeProxied     = "proxied"     // Answered by the application
	OutcomeWoken       = "woken"       // Waited for the application to wake up, then answered by it
	OutcomeWakingPage  = "waking_page" // A browser got the "waking up" page
	OutcomeAsleep      = "asleep"      // Ignored (monitor) and answered by Smart Proxy while asleep
	OutcomeUnavailable = "unavailable" // The application didn't start in time, or couldn't be woken
	OutcomeLogin       = "login"       // Sent to the route's login page
	OutcomeDenied      = "denied"      // Rejected: no valid credentials
	OutcomeError       = "error"       // The application couldn't be reached
)

// Entry is one recorded request.
type Entry struct {
	At         time.Time         `json:"at"`
	Replica    string            `json:"replica"`
	Method     string            `json:"method"`
	Host       string            `json:"host"`
	Path       string            `json:"path"`
	Query      []string          `json:"query,omitempty"` // Parameter names
	Status     int               `json:"status"`
	DurationMs float64           `json:"duration_ms"`
	Outcome    string            `json:"outcome"`
	Backend    string            `json:"backend,omitempty"` // Service it went to
	Why        string            `json:"why,omitempty"`     // Why that backend
	Ignored    string            `json:"ignored,omitempty"` // Why it didn't count as activity
	User       string            `json:"user,omitempty"`    // Authenticated user or token
	Client     string            `json:"client"`
	UserAgent  string            `json:"user_agent,omitempty"`
	Headers    map[string]string `json:"headers"`
	Cookies    []string          `json:"cookies,omitempty"` // Names
}

const perRoute = 300 // Latest requests kept per route and replica

// Recorder keeps the latest requests of each route.
type Recorder struct {
	Replica string

	mu     sync.Mutex
	routes map[string][]Entry // Oldest first
}

// Add records a request.
func (r *Recorder) Add(routeID string, e Entry) {
	e.Replica = r.Replica
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes == nil {
		r.routes = map[string][]Entry{}
	}
	list := append(r.routes[routeID], e)
	if len(list) > perRoute {
		list = append([]Entry(nil), list[len(list)-perRoute:]...)
	}
	r.routes[routeID] = list
}

// Since returns a route's requests recorded after a time, oldest first.
func (r *Recorder) Since(routeID string, after time.Time) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Entry
	for _, e := range r.routes[routeID] {
		if e.At.After(after) {
			out = append(out, e)
		}
	}
	return out
}

// Clear forgets a route's requests.
func (r *Recorder) Clear(routeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, routeID)
}

// Merge sorts the requests of several replicas by time, newest last, keeping at most limit.
func Merge(limit int, lists ...[]Entry) []Entry {
	var all []Entry
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

// sensitive matches headers whose values are credentials.
var sensitive = regexp.MustCompile(`(?i)(authorization|cookie|token|secret|password|passwd|api-?key|session|signature|credential)`)

// Masked is shown in place of a credential.
const Masked = "••••••"

// Describe fills an entry with what can be shown of a request: headers (credentials masked),
// cookie and query parameter names.
func Describe(r *http.Request, client net.IP) Entry {
	e := Entry{
		At: time.Now(), Method: r.Method, Host: r.Host, Path: r.URL.Path,
		UserAgent: r.UserAgent(), Headers: map[string]string{},
	}
	if client != nil {
		e.Client = client.String()
	}
	for name := range r.URL.Query() {
		e.Query = append(e.Query, name)
	}
	sort.Strings(e.Query)
	for name, values := range r.Header {
		switch {
		case name == "Cookie":
			continue
		case sensitive.MatchString(name):
			e.Headers[name] = Masked
		default:
			e.Headers[name] = truncate(strings.Join(values, ", "), 500)
		}
	}
	for _, c := range r.Cookies() {
		e.Cookies = append(e.Cookies, c.Name)
	}
	sort.Strings(e.Cookies)
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// StatusWriter remembers the status written through it. It unwraps to the original writer, so
// flushing and connection upgrades (WebSockets) keep working.
type StatusWriter struct {
	http.ResponseWriter
	Status int
}

func (w *StatusWriter) WriteHeader(code int) {
	if w.Status == 0 {
		w.Status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *StatusWriter) Write(b []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *StatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps streaming working for code checking http.Flusher directly.
func (w *StatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// PeerPath is where a replica serves its recorded requests to the others (on the metrics port,
// with the shared peer token).
const PeerPath = "/internal/requests"

// PeerHandler serves this replica's recorded requests to the others.
func (r *Recorder) PeerHandler(token func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		want := token()
		got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if want == "" || !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		list := r.Since(req.URL.Query().Get("id"), time.Time{})
		if list == nil {
			list = []Entry{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})
}
