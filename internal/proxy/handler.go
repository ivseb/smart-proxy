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
	TotalRequests int64
	RouteStats    map[string]int64 // Key: Route ID
}

func NewMetrics() *Metrics {
	return &Metrics{
		RouteStats: make(map[string]int64),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Special Endpoint: Status Check
	if r.URL.Path == "/__smart_proxy/status" {
		h.handleStatusCheck(w, r)
		return
	}

	// 1. Match Route (Host + Path)
	var matchedRoute store.RouteConfig
	var matchedPath string
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
		hostMatches := route.Host == "" || route.Host == requestHost

		if hostMatches && strings.HasPrefix(r.URL.Path, route.Path) {
			// Priority:
			// 1. Longer Path wins
			// 2. Specific Host wins over empty Host (if paths are same length)

			isBetterMatch := false
			if !found {
				isBetterMatch = true
			} else {
				if len(route.Path) > len(matchedPath) {
					isBetterMatch = true
				} else if len(route.Path) == len(matchedPath) && route.Host != "" && matchedRoute.Host == "" {
					isBetterMatch = true
				}
			}

			if isBetterMatch {
				matchedRoute = route
				matchedPath = route.Path
				found = true
			}
		}
	}

	// If no route matched
	if !found {
		http.NotFound(w, r)
		return
	}

	// Update Activity
	h.store.UpdateActivity(matchedRoute.ID) // Using ID for updates is cleaner if unique, otherwise Path+Host?
	// Note: Store update logic might need to handle ID. Let's assume Path is still unique-ish or just update based on found route object which has ID.
	// Actually previous code used matchedPath, but store might rely on Key.
	// Let's stick to Path for now or update Store to use ID.
	// For V2, let's assume we pass the Route ID to update activity if possible,
	// but the Store interface currently takes "path".
	// Let's rely on the Store implementation to handle unique identification.
	// If Store uses Path as key, this breaks with Host.
	// FIXME: Store needs to key by ID or Host+Path.
	// For this step, I will pass the Path but we should refactor API to use ID.
	// Hacking it for now:
	h.store.UpdateActivity(matchedRoute.Path)

	logger.Printf("Request: %s (Host: %s) -> Route: %s (Deps: %v)", r.URL.Path, r.Host, matchedRoute.Deployment, matchedRoute.Dependencies)

	// 2. Check Chain Status
	// We need to check the Main Deployment AND all Dependencies
	deploymentsToCheck := []string{matchedRoute.Deployment}
	for _, d := range matchedRoute.Dependencies {
		deploymentsToCheck = append(deploymentsToCheck, d.Name)
	}

	allReady := true

	for _, depName := range deploymentsToCheck {
		// Assume dependencies are in the same namespace for now
		// In a real V2, deps might be "namespace/name" string.
		// For simplicity V2.0, same namespace.
		targetNs := matchedRoute.Namespace

		replicas, readyReplicas, err := h.k8sClient.GetDeploymentStatus(targetNs, depName)
		if err != nil {
			log.Printf("Error getting status for %s: %v", depName, err)
			continue // Don't block everything on status error, or maybe we should?
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
	targetURLStr := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", matchedRoute.TargetService, matchedRoute.Namespace, matchedRoute.TargetPort)
	targetURL, err := url.Parse(targetURLStr)
	if err != nil {
		logger.Printf("Invalid target URL: %v", err)
		http.Error(w, "Invalid configuration", http.StatusInternalServerError)
		return
	}

	// Track Metrics
	h.Metrics.TotalRequests++
	if matchedRoute.ID != "" {
		h.Metrics.RouteStats[matchedRoute.ID]++
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	if matchedRoute.InjectBadge {
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
	if matchedRoute.InjectBadge {
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

	// Find Route (Duplicate logic, ideally refactor finding logic)
	var matchedRoute store.RouteConfig
	var matchedPath string
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

		hostMatches := route.Host == "" || route.Host == checkHost
		if hostMatches && strings.HasPrefix(path, route.Path) {
			isBetterMatch := false
			if !found {
				isBetterMatch = true
			} else {
				if len(route.Path) > len(matchedPath) {
					isBetterMatch = true
				} else if len(route.Path) == len(matchedPath) && route.Host != "" && matchedRoute.Host == "" {
					isBetterMatch = true
				}
			}
			if isBetterMatch {
				matchedRoute = route
				matchedPath = route.Path
				found = true
			}
		}
	}

	if !found {
		http.NotFound(w, r)
		return
	}

	// Check ALL Dependencies
	deploymentsToCheck := []string{matchedRoute.Deployment}
	for _, d := range matchedRoute.Dependencies {
		deploymentsToCheck = append(deploymentsToCheck, d.Name)
	}
	allReady := true

	type ServiceStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"` // Ready, Scaling, Sleep, Error
	}
	var details []ServiceStatus

	for _, depName := range deploymentsToCheck {
		replicas, readyReplicas, err := h.k8sClient.GetDeploymentStatus(matchedRoute.Namespace, depName)
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
	if h.tmpl != nil {
		h.tmpl.Execute(w, nil)
	} else {
		w.Write([]byte("<h1>Waking up... please wait...</h1><script>setTimeout(() => location.reload(), 2000)</script>"))
	}
}
