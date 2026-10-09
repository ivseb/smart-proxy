// Package admin implements the administrative HTTP server for the Smart Proxy.
// It provides API endpoints for managing routes, viewing logs, and interacting with Kubernetes resources.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/history"
	"smart-proxy/internal/inspect"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
	"smart-proxy/internal/vault"
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
	// GlobalRules are the requests ignored for every route (shown in the dashboard).
	GlobalRules traffic.Rules
	// Traffic, when set, returns who sends requests to a route (across replicas).
	Traffic func(routeID string) []traffic.SourceStats
	// History, when set, holds recent request rates for the charts.
	History *history.Recorder
	// Inspect holds this replica's recorded requests; Peers returns the base URLs of the other
	// replicas, asked for theirs with PeerToken.
	Inspect   *inspect.Recorder
	Peers     func() []string
	PeerToken func() string
	// Vault returns Smart Proxy's Secret (nil while unavailable): credentials of protected routes.
	Vault func() *vault.Vault
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
	mux.HandleFunc("/api/routes/traffic", s.handleRouteTraffic)
	mux.HandleFunc("/api/routes/inspect", s.handleInspect)
	mux.HandleFunc("/api/routes/requests", s.handleRequests)
	mux.HandleFunc("/api/routes/protection/users", s.handleProtectionUsers)
	mux.HandleFunc("/api/routes/protection/tokens", s.handleProtectionTokens)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/stats/history", s.handleStatsHistory)
	mux.HandleFunc("/api/logs", s.handleLogs)

	mux.HandleFunc("/api/k8s/namespaces", s.requireK8s(s.handleNamespaces))
	mux.HandleFunc("/api/k8s/deployments", s.requireK8s(s.handleDeployments))
	mux.HandleFunc("/api/k8s/ingresses", s.requireK8s(s.handleIngresses))
	mux.HandleFunc("/api/k8s/routes", s.requireK8s(s.handleOpenshiftRoutes))
	mux.HandleFunc("/api/k8s/stop-deployment", s.requireK8s(s.handleStopDeployment))
	mux.HandleFunc("/api/k8s/wake-deployment", s.requireK8s(s.handleWakeDeployment))
	mux.HandleFunc("/api/k8s/deployment-service-info", s.requireK8s(s.handleDeploymentServiceInfo))
	mux.HandleFunc("/api/k8s/services", s.requireK8s(s.handleServices))
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
		"connected":       s.k8sClient != nil,
		"scope":           "",
		"all_namespaces":  false,
		"namespaces":      []string{},
		"default":         "",
		"routes_enabled":  false,
		"proxy_service":   s.ServiceName,
		"replica":         s.Replica,
		"ignore_defaults": s.GlobalRules,
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

// handleStatsHistory returns the requests received in each recent interval, oldest first.
func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, map[string]any{"interval": 0, "points": []history.Point{}})
		return
	}
	points := s.History.Points()
	if points == nil {
		points = []history.Point{}
	}
	writeJSON(w, map[string]any{"interval": s.History.Interval.Seconds(), "retention": s.History.Retention.Seconds(), "points": points})
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

// ServiceInfo is a Service a route can send traffic to, with the workload behind it.
type ServiceInfo struct {
	Name     string        `json:"name"`
	Ports    []ServicePort `json:"ports"`
	Workload string        `json:"workload,omitempty"`
}

// ServicePort is one port of a Service.
type ServicePort struct {
	Name string `json:"name,omitempty"`
	Port int32  `json:"port"`
}

// handleServices lists a namespace's Services (except Smart Proxy's own stand-in), to add one
// as a backend of a route.
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.namespaceParam(w, r)
	if !ok {
		return
	}
	services, err := s.k8sClient.ListServices(ns)
	if err != nil {
		http.Error(w, err.Error(), httpStatusFor(err))
		return
	}
	result := []ServiceInfo{}
	for _, svc := range services {
		if k8s.IsProxyService(svc.Name, s.ServiceName) {
			continue
		}
		info := ServiceInfo{Name: svc.Name}
		for _, p := range svc.Spec.Ports {
			info.Ports = append(info.Ports, ServicePort{Name: p.Name, Port: p.Port})
		}
		info.Workload, _ = s.k8sClient.ResolveDeploymentForService(ns, svc.Name)
		result = append(result, info)
	}
	writeJSON(w, result)
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
		// Share is the percentage of the Route's weight going to this Service (100 when it is
		// the only one); Alternate is true when it is one of its alternate backends.
		Share     float64 `json:"share"`
		Alternate bool    `json:"alternate"`
	}
	matching := []RouteInfo{}
	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			if rt.Namespace != ns {
				continue
			}
			targets, _ := k8s.OriginalRouteTargets(rt)
			var total int32
			for _, t := range targets {
				total += t.Weight
			}
			for i, t := range targets {
				if t.Service == svc {
					share := 100.0
					if total > 0 {
						share = float64(t.Weight) * 100 / float64(total)
					}
					matching = append(matching, RouteInfo{Name: rt.Name, Host: k8s.RouteHost(rt), Type: store.KindRoute, Share: share, Alternate: i > 0})
				}
			}
		}
	}
	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			if b, ok := k8s.OriginalIngressBackend(ing); ok && ing.Namespace == ns && b.Service == svc {
				matching = append(matching, RouteInfo{Name: ing.Name, Host: k8s.IngressHost(ing), Type: store.KindIngress, Share: 100})
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

	slept, err := s.k8sClient.SleepDeployment(namespace, deployment)
	if slept {
		metrics.Slept(namespace, deployment, "manual")
	}
	if err != nil {
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
			metrics.WakeStarted(namespace, name, "manual")
		}
	}
	w.WriteHeader(http.StatusOK)
}

// SourceView is a client of a route, as shown in the dashboard.
type SourceView struct {
	traffic.SourceStats
	// IntervalSeconds is the average time between its requests (0 when unknown).
	IntervalSeconds float64 `json:"interval_seconds"`
	// CountsAsActivity is true while its requests keep the route awake (its latest one counted).
	CountsAsActivity bool `json:"counts_as_activity"`
}

// handleRouteTraffic lists who sends requests to a route, to find what keeps it awake.
func (s *Server) handleRouteTraffic(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}
	views := []SourceView{}
	if s.Traffic != nil {
		for _, src := range s.Traffic(id) {
			views = append(views, SourceView{
				SourceStats:      src,
				IntervalSeconds:  src.Interval().Seconds(),
				CountsAsActivity: !src.LastIgnored,
			})
		}
	}
	writeJSON(w, views)
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
