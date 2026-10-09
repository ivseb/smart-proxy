// Package admin implements the administrative HTTP server for the Smart Proxy.
// It provides API endpoints for managing routes, viewing logs, and interacting with Kubernetes resources.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"
)

// Server represents the admin HTTP server.
type Server struct {
	k8sClient *k8s.Client
	store     *store.Store
	Metrics   *proxy.Metrics
	// ServiceName is the Service fronting Smart Proxy; patched Ingresses/Routes point at it.
	ServiceName string
	auth        *auth.Auth

	// RequestTotals, when set, returns request counts across all replicas.
	RequestTotals func() (int64, map[string]int64)
	// Replica identifies this pod (its log stream only shows its own logs).
	Replica string
}

// NewServer creates a new instance of the admin Server.
func NewServer(k8sClient *k8s.Client, store *store.Store, metrics *proxy.Metrics, serviceName string, authn *auth.Auth) *Server {
	return &Server{
		k8sClient:   k8sClient,
		store:       store,
		Metrics:     metrics,
		ServiceName: serviceName,
		auth:        authn,
	}
}

// Handler returns the admin dashboard and API, behind authentication.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static Files (Admin UI)
	mux.Handle("/", http.FileServer(http.Dir("web/static")))

	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/routes", s.handleRoutes)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/logs", s.handleLogs)

	mux.HandleFunc("/api/k8s/namespaces", s.requireK8s(s.handleNamespaces))
	mux.HandleFunc("/api/k8s/deployments", s.requireK8s(s.handleDeployments))
	mux.HandleFunc("/api/k8s/ingresses", s.requireK8s(s.handleIngresses))
	mux.HandleFunc("/api/k8s/routes", s.requireK8s(s.handleOpenshiftRoutes))
	mux.HandleFunc("/api/k8s/stop-deployment", s.requireK8s(s.handleStopDeployment))
	mux.HandleFunc("/api/k8s/wake-deployment", s.requireK8s(s.handleWakeDeployment))
	mux.HandleFunc("/api/k8s/deployment-service-info", s.requireK8s(s.handleDeploymentServiceInfo))
	mux.HandleFunc("/api/k8s/service-routes", s.requireK8s(s.handleServiceRoutes))

	mux.HandleFunc("/api/patch-ingress", s.requireK8s(s.handlePatchIngress))
	mux.HandleFunc("/api/unpatch-ingress", s.requireK8s(s.handleUnpatchIngress))
	mux.HandleFunc("/api/patch-route", s.requireK8s(s.handlePatchRoute))
	mux.HandleFunc("/api/unpatch-route", s.requireK8s(s.handleUnpatchRoute))

	// Login/logout endpoints; everything else requires authentication.
	s.auth.RegisterRoutes(mux)

	return s.auth.Wrap(mux)
}

// requireK8s answers 503 instead of calling handlers that need the cluster when the
// Kubernetes client could not be created (offline mode).
func (s *Server) requireK8s(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.k8sClient == nil {
			http.Error(w, "Kubernetes client unavailable", http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	}
}

// namespaceParam returns the ?namespace= of a request (defaulting for older clients) and
// rejects namespaces Smart Proxy doesn't manage.
func (s *Server) namespaceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = s.k8sClient.DefaultNamespace()
	}
	if !s.k8sClient.Watches(ns) {
		http.Error(w, fmt.Sprintf("Namespace %q is not managed by Smart Proxy", ns), http.StatusForbidden)
		return "", false
	}
	return ns, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// handleInfo describes what this Smart Proxy manages, for the dashboard.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{
		"connected":      s.k8sClient != nil,
		"scope":          "",
		"all_namespaces": false,
		"namespaces":     []string{},
		"default":        "",
		"routes_enabled": false,
		"proxy_service":  s.ServiceName,
		"replica":        s.Replica,
	}
	if s.k8sClient != nil {
		info["scope"] = s.k8sClient.Scope().String()
		info["all_namespaces"] = s.k8sClient.Scope().All
		info["namespaces"] = s.k8sClient.WatchedNamespaces()
		info["default"] = s.k8sClient.DefaultNamespace()
		info["routes_enabled"] = s.k8sClient.RoutesEnabled()
	}
	writeJSON(w, info)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	// SSE Handler
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientChan := logger.Get().Subscribe()
	defer logger.Get().Unsubscribe(clientChan)

	// Send history first
	for _, entry := range logger.Get().GetHistory() {
		data, _ := json.Marshal(entry)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	w.(http.Flusher).Flush()

	// Stream new logs
	for {
		select {
		case entry := <-clientChan:
			data, _ := json.Marshal(entry)
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.RequestTotals != nil {
		total, routes := s.RequestTotals()
		writeJSON(w, map[string]any{"TotalRequests": total, "RouteStats": routes})
		return
	}
	if s.Metrics != nil {
		writeJSON(w, s.Metrics)
	} else {
		writeJSON(w, map[string]any{})
	}
}

// handleNamespaces lists the namespaces Smart Proxy manages.
func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.k8sClient.WatchedNamespaces())
}

// handleDeployments lists the Deployments of a namespace that can be put behind Smart Proxy.
func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}
	deployments, err := s.k8sClient.ListDeployments(ns)
	if err != nil {
		logger.Printf("Error listing deployments in %s: %v", ns, err)
		http.Error(w, "Failed to list deployments: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Smart Proxy's own Deployment shares the Service's name; never offer it as a target.
	targets := make([]string, 0, len(deployments))
	for _, d := range deployments {
		if d != s.ServiceName {
			targets = append(targets, d)
		}
	}
	writeJSON(w, targets)
}

func (s *Server) handleDeploymentServiceInfo(w http.ResponseWriter, r *http.Request) {
	dep := r.URL.Query().Get("deployment")
	if dep == "" {
		http.Error(w, "Missing deployment", http.StatusBadRequest)
		return
	}
	ns, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}
	svcName, port, err := s.k8sClient.ResolveServiceForDeployment(ns, dep)
	if err != nil {
		writeJSON(w, map[string]any{"service": "", "port": 80, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"service": svcName, "port": port})
}

// handleServiceRoutes lists the Ingresses and Routes that send traffic to a Service.
func (s *Server) handleServiceRoutes(w http.ResponseWriter, r *http.Request) {
	svc := r.URL.Query().Get("service")
	if svc == "" {
		http.Error(w, "Missing service", http.StatusBadRequest)
		return
	}
	ns, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}

	type RouteInfo struct {
		Name string `json:"name"`
		Host string `json:"host"`
		Type string `json:"type"`
	}
	matching := []RouteInfo{}
	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			if rt.Namespace == ns && k8s.OriginalRouteService(rt) == svc {
				matching = append(matching, RouteInfo{Name: rt.Name, Host: rt.Spec.Host, Type: store.KindRoute})
			}
		}
	}
	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			if b, ok := k8s.OriginalIngressBackend(ing); ok && ing.Namespace == ns && b.Service == svc {
				matching = append(matching, RouteInfo{Name: ing.Name, Host: k8s.IngressHost(ing), Type: store.KindIngress})
			}
		}
	}
	writeJSON(w, matching)
}

func (s *Server) handleStopDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deployment := r.URL.Query().Get("deployment")
	if deployment == "" {
		http.Error(w, "Missing deployment", http.StatusBadRequest)
		return
	}
	namespace, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}

	if _, err := s.k8sClient.SleepDeployment(namespace, deployment); err != nil {
		logger.Printf("Error scaling down %s/%s: %v", namespace, deployment, err)
		status := http.StatusInternalServerError
		if errors.Is(err, k8s.ErrManagedByKEDA) {
			status = http.StatusConflict
		}
		http.Error(w, fmt.Sprintf("Cannot stop %s: %v", deployment, err), status)
		return
	}
	logger.Printf("Manual shutdown triggered for %s/%s", namespace, deployment)

	// Stop dependencies if configured
	for _, route := range s.store.GetAllRoutes() {
		if route.Namespace != namespace || route.Deployment != deployment {
			continue
		}
		for _, dep := range route.Dependencies {
			if dep.StopOnIdle {
				logger.Printf("Stopping dependency %s for manual stop of %s", dep.Name, deployment)
				if _, err := s.k8sClient.SleepDeployment(namespace, dep.Name); err != nil {
					logger.Printf("Error stopping dependency %s: %v", dep.Name, err)
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleWakeDeployment wakes a deployment (and the dependencies of its routes) ahead of traffic.
// Its routes count as active, so the watcher doesn't put it straight back to sleep.
func (s *Server) handleWakeDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deployment := r.URL.Query().Get("deployment")
	if deployment == "" {
		http.Error(w, "Missing deployment", http.StatusBadRequest)
		return
	}
	namespace, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}

	toWake := []string{deployment}
	for _, route := range s.store.GetAllRoutes() {
		if route.Namespace == namespace && route.Deployment == deployment {
			s.store.UpdateActivity(route.ID)
			for _, dep := range route.Dependencies {
				toWake = append(toWake, dep.Name)
			}
		}
	}
	for _, name := range toWake {
		target, err := s.k8sClient.WakeDeployment(namespace, name)
		if err != nil {
			logger.Printf("Error waking %s/%s: %v", namespace, name, err)
			http.Error(w, fmt.Sprintf("Cannot wake %s: %v", name, err), httpStatusFor(err))
			return
		}
		if target > 0 {
			logger.Printf("Manual wake-up of %s/%s with %d replica(s)", namespace, name, target)
		}
	}
	w.WriteHeader(http.StatusOK)
}
