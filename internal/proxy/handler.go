// Package proxy implements the reverse proxy logic, including route matching,
// idle detection, and response modification (e.g., badge injection).
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

type Handler struct {
	k8sClient *k8s.Client
	store     *store.Store
	tmpl      *template.Template
	Metrics   *Metrics
	ready     atomic.Bool
	draining  atomic.Bool

	// GlobalRules select requests that never count as activity, for every route.
	GlobalRules traffic.Rules
	// TrustedProxies decide when X-Forwarded-For names the client.
	TrustedProxies traffic.TrustedProxies
	// Traffic records who sends requests to each route (nil disables it).
	Traffic *traffic.Recorder
	// Transport, when set, replaces the default transport to the applications (tests).
	Transport http.RoundTripper
	// WakeTimeout is how long requests other than page loads wait for a sleeping app
	// (default 2 minutes).
	WakeTimeout time.Duration

	inflight inflight
}

// Probe endpoints, under the reserved /__smart_proxy/ prefix so they can't shadow an
// application's own paths. Liveness only says the process is up; readiness also requires
// the Kubernetes caches to be synced and no shutdown in progress.
const (
	HealthPath = "/__smart_proxy/healthz"
	ReadyPath  = "/__smart_proxy/readyz"
)

// SetReady marks the proxy as able to serve traffic (caches synced).
func (h *Handler) SetReady() {
	h.ready.Store(true)
}

// SetDraining makes the readiness check fail so Kubernetes stops sending traffic before shutdown.
func (h *Handler) SetDraining() {
	h.draining.Store(true)
}

func NewHandler(k8sClient *k8s.Client, store *store.Store) *Handler {
	tmpl, err := template.ParseFiles("web/templates/loading.html")
	if err != nil {
		logger.Printf("Warning: Could not parse loading template: %v", err)
	}

	return &Handler{
		k8sClient: k8sClient,
		store:     store,
		tmpl:      tmpl,
		Metrics:   NewMetrics(),
	}
}

type Metrics struct {
	mu            sync.RWMutex
	TotalRequests int64            `json:"TotalRequests"`
	RouteStats    map[string]int64 `json:"RouteStats"`
}

func NewMetrics() *Metrics {
	return &Metrics{
		RouteStats: make(map[string]int64),
	}
}

func (m *Metrics) Increment(routeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalRequests++
	if routeID != "" {
		m.RouteStats[routeID]++
	}
}

// Snapshot returns the total and per-route request counts.
func (m *Metrics) Snapshot() (int64, map[string]int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	routes := make(map[string]int64, len(m.RouteStats))
	for k, v := range m.RouteStats {
		routes[k] = v
	}
	return m.TotalRequests, routes
}

func (m *Metrics) MarshalJSON() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Clone the map to avoid race condition during json.Marshal
	statsCopy := make(map[string]int64, len(m.RouteStats))
	for k, v := range m.RouteStats {
		statsCopy[k] = v
	}

	return json.Marshal(&struct {
		TotalRequests int64            `json:"TotalRequests"`
		RouteStats    map[string]int64 `json:"RouteStats"`
	}{
		TotalRequests: m.TotalRequests,
		RouteStats:    statsCopy,
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == HealthPath:
		w.Write([]byte("ok"))
		return
	case r.URL.Path == ReadyPath:
		switch {
		case h.draining.Load():
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
		case !h.ready.Load():
			http.Error(w, "starting", http.StatusServiceUnavailable)
		default:
			w.Write([]byte("ok"))
		}
		return
	case h.k8sClient == nil:
		// Without a cluster connection nothing can be woken or proxied.
		http.Error(w, "Smart Proxy: Kubernetes client unavailable", http.StatusServiceUnavailable)
		return
	case !h.ready.Load():
		http.Error(w, "Smart Proxy is starting", http.StatusServiceUnavailable)
		return
	}

	// Special Endpoint: Status Check
	if strings.HasSuffix(r.URL.Path, "/__smart_proxy/status") {
		h.handleStatusCheck(w, r)
		return
	}

	// 1. Find the route for this host and path.
	route, found := h.matchRoute(requestHost(r), r.URL.Path)
	if !found {
		http.NotFound(w, r)
		return
	}

	// Uptime monitors, health checks and probes don't count as activity, and don't wake
	// anything: while the route sleeps they get an answer from Smart Proxy (see WhenAsleep).
	client := h.TrustedProxies.ClientIP(r)
	reason := h.ignoredReason(r, client, route)
	if h.Traffic != nil {
		h.Traffic.Record(route.ID, r, client, reason)
	}
	wake := true
	if reason == "" {
		h.store.UpdateActivity(route.ID)
		// Long requests (WebSockets, streams, downloads) keep the route active until they end.
		defer h.track(route.ID)()
	} else {
		metrics.Ignored(route.Namespace, route.ID, reason)
		if h.serving(route) {
			wake = false
		} else if h.answerAsleep(w, route) {
			return
		}
	}

	// 2. Wake what the route needs. While the managed workloads wake up, a running backend
	// Smart Proxy doesn't manage takes the traffic, as the OpenShift router would. Browsers get
	// the "waking up" page; other requests (API calls, form posts, WebSockets) wait for the app.
	woken := false
	if wake {
		if _, allReady := h.ensureAwake([]store.RouteConfig{route}); !allReady && !h.hasServingPassThrough(route) {
			if wantsPage(r) {
				h.serveLoadingPage(w)
				return
			}
			if !h.waitAwake(r.Context(), route) {
				if r.Context().Err() == nil {
					w.Header().Set("Retry-After", "10")
					http.Error(w, "Smart Proxy: the application is still starting", http.StatusServiceUnavailable)
				}
				return
			}
			woken = true
		}
	}

	// 3. Proxy the request.
	dest := h.pickTarget(w, r, route)
	host := fmt.Sprintf("%s.%s.svc.cluster.local:%d", dest.Service, route.Namespace, dest.Port)
	if woken && h.Transport == nil {
		waitReachable(r.Context(), host, 15*time.Second)
	}

	h.Metrics.Increment(route.ID)
	metrics.Request(route.Namespace, route.ID)

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: host})
	proxy.Transport = transport
	if h.Transport != nil {
		proxy.Transport = h.Transport
	}
	proxy.ErrorHandler = proxyError
	if route.InjectBadge && r.Method != http.MethodHead {
		proxy.ModifyResponse = injectBadge
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Del("Accept-Encoding") // Plain responses, so the badge can be added
		}
	}

	proxy.ServeHTTP(w, r)
}

// maxBadgeBody is the largest HTML page the badge is added to (it is buffered in memory).
const maxBadgeBody = 4 << 20

const badgeHTML = `
<div style="position:fixed;bottom:12px;right:12px;display:flex;align-items:center;gap:8px;padding:8px 12px;background:rgba(15, 23, 42, 0.95);border:1px solid rgba(59, 130, 246, 0.5);border-radius:99px;color:#cbd5e1;font-family:'Inter',system-ui,sans-serif;font-size:12px;font-weight:500;box-shadow:0 4px 12px rgba(0,0,0,0.3);z-index:99999;backdrop-filter:blur(8px);pointer-events:none;user-select:none;">
    <span style="color:#3b82f6;font-size:14px;">⚡</span>
    <span>Powered by <span style="color:#fff;font-weight:600;">Smart Proxy</span></span>
</div>`

// injectBadge adds the "Powered by Smart Proxy" badge to HTML pages.
func injectBadge(resp *http.Response) error {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") || resp.Header.Get("Content-Encoding") != "" ||
		resp.StatusCode < 200 || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified ||
		resp.ContentLength > maxBadgeBody {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBadgeBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBadgeBody {
		// Too large to buffer: pass it through unchanged.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	resp.Body.Close()

	page := string(body)
	if i := strings.LastIndex(page, "</body>"); i >= 0 {
		page = page[:i] + badgeHTML + page[i:]
	} else {
		page += badgeHTML
	}
	resp.Body = io.NopCloser(strings.NewReader(page))
	resp.ContentLength = int64(len(page))
	resp.Header.Set("Content-Length", fmt.Sprint(len(page)))
	// Disable caching of modified content
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	return nil
}

func (h *Handler) handleStatusCheck(w http.ResponseWriter, r *http.Request) {
	// Status check now needs to know the Host header too to find the right route
	// The client JS might not send the Host header of the original request easily
	// unless we embed it in the URL parameters.

	path := r.URL.Query().Get("path")

	if path == "" {
		if strings.HasSuffix(r.URL.Path, "/__smart_proxy/status") {
			path = strings.TrimSuffix(r.URL.Path, "/__smart_proxy/status")
		}
		if path == "" {
			path = "/"
		}
	}

	// Only the request's own host: the page polling this is served under it.
	route, found := h.matchRoute(requestHost(r), path)
	if !found {
		http.NotFound(w, r)
		return
	}
	matchedRoutes := []store.RouteConfig{route}

	// Keep waking: with StartInOrder, each poll of the waiting page advances the chain.
	details, allReady := h.ensureAwake(matchedRoutes)

	if allReady {
		for _, route := range matchedRoutes {
			targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", route.TargetService, route.Namespace, route.TargetPort, route.Path)
			client := &http.Client{
				Timeout: 1000 * time.Millisecond,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			req, err := http.NewRequest("GET", targetURL, nil)
			if err == nil {
				req.Header.Set("User-Agent", "Smart-Proxy-Warmup-Probe")
				resp, err := client.Do(req)
				if err != nil {
					allReady = false
					details = append(details, workloadState{
						Name:   route.TargetService + " (warming up)",
						Status: stateScaling,
					})
				} else {
					resp.Body.Close()
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":  "waiting",
		"details": details,
	}
	if allReady {
		response["status"] = "ready"
	}

	json.NewEncoder(w).Encode(response)
}

func (h *Handler) serveLoadingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	if h.tmpl != nil {
		h.tmpl.Execute(w, nil)
	} else {
		w.Write([]byte("<h1>Waking up... please wait...</h1><script>setTimeout(() => location.reload(), 2000)</script>"))
	}
}
