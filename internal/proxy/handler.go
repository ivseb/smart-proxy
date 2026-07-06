// Package proxy implements the reverse proxy logic, including route matching,
// idle detection, and response modification (e.g., badge injection).
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

type Handler struct {
	k8sClient *k8s.Client
	store     *store.Store
	tmpl      *template.Template
	Metrics   *Metrics
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
	// Special Endpoint: Status Check
	if r.URL.Path == "/__smart_proxy/status" {
		h.handleStatusCheck(w, r)
		return
	}

	// 1. Match Routes (Host + Path)
	var matchedRoutes []store.RouteConfig
	var bestRoute store.RouteConfig
	var bestPath string
	found := false

	routes := h.store.GetAllRoutes()
	for _, route := range routes {
		// Host matching: If route.Host is set, it MUST match the request host.
		// If route.Host is empty, it matches any host (legacy behavior or catch-all).
		requestHost := r.Host
		if strings.Contains(requestHost, ":") {
			host, _, err := net.SplitHostPort(requestHost)
			if err == nil {
				requestHost = host
			}
		}
		hostMatches := matchHost(route.Host, requestHost)

		if hostMatches && strings.HasPrefix(r.URL.Path, route.Path) {
			matchedRoutes = append(matchedRoutes, route)

			// Priority:
			// 1. Longer Path wins
			// 2. Specific Host wins over empty Host (if paths are same length)
			isBetterMatch := false
			if !found {
				isBetterMatch = true
			} else {
				if len(route.Path) > len(bestPath) {
					isBetterMatch = true
				} else if len(route.Path) == len(bestPath) && route.Host != "" && bestRoute.Host == "" {
					isBetterMatch = true
				}
			}

			if isBetterMatch {
				bestRoute = route
				bestPath = route.Path
				found = true
			}
		}
	}

	// If no route matched
	if !found {
		http.NotFound(w, r)
		return
	}

	// Check if this request is a Kubernetes probe.
	// Probes should not keep the route/deployments alive, so we skip updating the activity timer.
	isProbe := false
	for _, route := range matchedRoutes {
		probePaths, err := h.k8sClient.GetDeploymentProbePaths(route.Namespace, route.Deployment)
		if err == nil {
			for _, p := range probePaths {
				if r.URL.Path == p {
					isProbe = true
					break
				}
			}
		}
		if isProbe {
			break
		}
		for _, dep := range route.Dependencies {
			depProbePaths, err := h.k8sClient.GetDeploymentProbePaths(route.Namespace, dep.Name)
			if err == nil {
				for _, p := range depProbePaths {
					if r.URL.Path == p {
						isProbe = true
						break
					}
				}
			}
			if isProbe {
				break
			}
		}
		if isProbe {
			break
		}
	}

	if !isProbe {
		for _, route := range matchedRoutes {
			h.store.UpdateActivity(route.ID)
		}
	} else {
		logger.Printf("Ignoring activity update for probe request: %s on route %s (Deployment: %s)", r.URL.Path, bestRoute.ID, bestRoute.Deployment)
	}

	logger.Printf("Request: %s (Host: %s) -> Best Route: %s (Deps: %v, Total Matched: %d)", r.URL.Path, r.Host, bestRoute.Deployment, bestRoute.Dependencies, len(matchedRoutes))

	// 2. Check Chain Status
	// We need to check the Main Deployment AND all Dependencies for ALL matched routes
	depMap := make(map[string]string) // name -> namespace
	for _, route := range matchedRoutes {
		depMap[route.Deployment] = route.Namespace
		for _, d := range route.Dependencies {
			depMap[d.Name] = route.Namespace
		}
	}

	allReady := true

	for depName, targetNs := range depMap {
		replicas, readyReplicas, err := h.k8sClient.GetDeploymentStatus(targetNs, depName)
		if err != nil {
			log.Printf("Error getting status for %s: %v", depName, err)
			continue
		}

		if replicas == 0 {
			logger.Printf("Dependency %s is sleeping. Waking up...", depName)
			err := h.k8sClient.ScaleDeployment(targetNs, depName, 1)
			if err != nil {
				logger.Printf("Error waking up %s: %v", depName, err)
			}
			allReady = false
		} else if readyReplicas == 0 {
			logger.Printf("Dependency %s is waking up...", depName)
			allReady = false
		}
	}

	if !allReady {
		h.serveLoadingPage(w)
		return
	}

	// 4. Proxy Request
	targetURLStr := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", bestRoute.TargetService, bestRoute.Namespace, bestRoute.TargetPort)
	targetURL, err := url.Parse(targetURLStr)
	if err != nil {
		logger.Printf("Invalid target URL: %v", err)
		http.Error(w, "Invalid configuration", http.StatusInternalServerError)
		return
	}

	// Track Metrics
	h.Metrics.Increment(bestRoute.ID)

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	if bestRoute.InjectBadge {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
				return nil
			}

			// Check for compression (not handling gzip here)
			if resp.Header.Get("Content-Encoding") != "" {
				return nil // Skip compressed responses
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			resp.Body.Close()

			badgeHTML := `
<div style="position:fixed;bottom:12px;right:12px;display:flex;align-items:center;gap:8px;padding:8px 12px;background:rgba(15, 23, 42, 0.95);border:1px solid rgba(59, 130, 246, 0.5);border-radius:99px;color:#cbd5e1;font-family:'Inter',system-ui,sans-serif;font-size:12px;font-weight:500;box-shadow:0 4px 12px rgba(0,0,0,0.3);z-index:99999;backdrop-filter:blur(8px);pointer-events:none;user-select:none;">
    <span style="color:#3b82f6;font-size:14px;">⚡</span>
    <span>Powered by <span style="color:#fff;font-weight:600;">Smart Proxy</span></span>
</div></body>`

			// Replace closing body tag, or append if not found
			newBodyStr := strings.Replace(string(body), "</body>", badgeHTML, 1)
			if !strings.Contains(newBodyStr, "Protected by Smart Proxy") { // Simple check to avoid double inject if replace failed?
				// Using "Powered by" as check string
				if !strings.Contains(newBodyStr, "Powered by") {
					newBodyStr += badgeHTML
				}
			}

			buf := bytes.NewBufferString(newBodyStr)
			resp.Body = io.NopCloser(buf)
			resp.ContentLength = int64(buf.Len())
			resp.Header.Set("Content-Length", fmt.Sprint(buf.Len()))

			// Disable caching of modified content
			resp.Header.Del("ETag")
			resp.Header.Del("Last-Modified")

			return nil
		}
	}

	// Force identity encoding to avoid GZIP so we can modify the body
	if bestRoute.InjectBadge {
		// We must modify the transport to not request compression,
		// OR just strip the header. Stripping header in Director is easiest.
		// However, httputil.ReverseProxy Director runs *before* we can easily set per-route logic
		// if we constructed it dynamically?
		// Actually, we create NewSingleHostReverseProxy here.

		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Del("Accept-Encoding") // Force backend to send plain text
		}
	}

	proxy.ServeHTTP(w, r)
}

func (h *Handler) handleStatusCheck(w http.ResponseWriter, r *http.Request) {
	// Status check now needs to know the Host header too to find the right route
	// The client JS might not send the Host header of the original request easily
	// unless we embed it in the URL parameters.

	path := r.URL.Query().Get("path")
	host := r.URL.Query().Get("host") // Client needs to send this

	if path == "" {
		http.Error(w, "Missing path", http.StatusBadRequest)
		return
	}

	// Find Routes
	var matchedRoutes []store.RouteConfig
	found := false
	routes := h.store.GetAllRoutes()
	for _, route := range routes {
		// Fix: Strip port from client-provided host param if present
		checkHost := host
		if strings.Contains(checkHost, ":") {
			h, _, err := net.SplitHostPort(checkHost)
			if err == nil {
				checkHost = h
			}
		}

		hostMatches := matchHost(route.Host, checkHost)
		if hostMatches && strings.HasPrefix(path, route.Path) {
			matchedRoutes = append(matchedRoutes, route)
			found = true
		}
	}

	if !found {
		http.NotFound(w, r)
		return
	}

	// Check ALL Dependencies for ALL matched routes
	depMap := make(map[string]string)
	for _, route := range matchedRoutes {
		depMap[route.Deployment] = route.Namespace
		for _, d := range route.Dependencies {
			depMap[d.Name] = route.Namespace
		}
	}
	allReady := true

	type ServiceStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"` // Ready, Scaling, Sleep, Error
	}
	var details []ServiceStatus

	for depName, targetNs := range depMap {
		replicas, readyReplicas, err := h.k8sClient.GetDeploymentStatus(targetNs, depName)
		status := "Unknown"
		if err != nil {
			status = "Error"
			allReady = false
		} else if replicas == 0 {
			status = "Sleep"
			allReady = false
		} else if readyReplicas < replicas {
			status = "Scaling"
			allReady = false
		} else {
			status = "Ready"
		}

		details = append(details, ServiceStatus{Name: depName, Status: status})
	}

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
					details = append(details, ServiceStatus{
						Name:   route.TargetService + " (warming up)",
						Status: "Scaling",
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

// matchHost checks if the requestHost matches a comma-separated list of route hosts (case-insensitive)
func matchHost(routeHost, requestHost string) bool {
	if routeHost == "" {
		return true
	}
	parts := strings.Split(routeHost, ",")
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part), requestHost) {
			return true
		}
	}
	return false
}
