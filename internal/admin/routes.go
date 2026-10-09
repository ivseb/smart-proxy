package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// Deployment states shown in the dashboard.
const (
	StatusReady     = "Ready"
	StatusScaling   = "Scaling"
	StatusSleep     = "Sleep"
	StatusError     = "Error"
	StatusUnwatched = "Unwatched" // The route's namespace is no longer managed by Smart Proxy
	StatusOffline   = "Offline"   // No Kubernetes connection
)

// ResourceRef identifies the Ingress or Route a route was created from.
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// RouteStatus is a route plus its live state, as listed by GET /api/routes.
type RouteStatus struct {
	store.RouteConfig
	Status           string            `json:"status"`
	Replicas         int32             `json:"replicas"`
	ReadyReplicas    int32             `json:"ready_replicas"`
	DependencyStatus map[string]string `json:"dependency_status"`
	// Source is the patched Ingress/Route, or null for manually configured routes.
	Source *ResourceRef `json:"source"`
	// SleepsAt is when the deployment will be scaled down without new traffic (null when it
	// never sleeps: Always On, manual routes, or already asleep).
	SleepsAt *time.Time `json:"sleeps_at"`
	// EffectiveIdleTimeout is the timeout in force, in nanoseconds (idle_timeout may be 0).
	EffectiveIdleTimeout time.Duration `json:"effective_idle_timeout"`
	// ScheduleActive is true while the route's schedule keeps it awake.
	ScheduleActive bool `json:"schedule_active"`
	// Resources are the Ingresses/Routes currently patched for this route (one per host);
	// deleting the route restores all of them.
	Resources []patchedResource `json:"resources"`
	// BackendStatus is the live state of each backend, when traffic is balanced across several.
	BackendStatus []BackendState `json:"backend_status"`
}

// BackendState is a backend of a route with its live state.
type BackendState struct {
	store.WeightedBackend
	Status string `json:"status"`
	// Share is the percentage of traffic it gets now (0 while it doesn't run).
	Share float64 `json:"share"`
}

func (s *Server) deploymentStatus(namespace, name string) (string, int32, int32) {
	if s.k8sClient == nil {
		return StatusOffline, 0, 0
	}
	if !s.k8sClient.Watches(namespace) {
		return StatusUnwatched, 0, 0
	}
	replicas, ready, err := s.k8sClient.GetDeploymentStatus(namespace, name)
	switch {
	case err != nil:
		return StatusError, 0, 0
	case replicas == 0:
		return StatusSleep, replicas, ready
	case ready < replicas:
		return StatusScaling, replicas, ready
	default:
		return StatusReady, replicas, ready
	}
}

func (s *Server) routeStatus(r store.RouteConfig) RouteStatus {
	rs := RouteStatus{
		RouteConfig:          r,
		DependencyStatus:     map[string]string{},
		EffectiveIdleTimeout: r.EffectiveIdleTimeout(),
	}
	rs.Status, rs.Replicas, rs.ReadyReplicas = s.deploymentStatus(r.Namespace, r.Deployment)
	for _, dep := range r.Dependencies {
		rs.DependencyStatus[dep.Name], _, _ = s.deploymentStatus(r.Namespace, dep.Name)
	}
	rs.ScheduleActive = r.ScheduledAwake(time.Now())
	rs.BackendStatus = s.backendStates(r)
	if kind, ns, name, ok := r.Resource(); ok {
		rs.Source = &ResourceRef{Kind: kind, Namespace: ns, Name: name}
		if !r.AlwaysOn && !rs.ScheduleActive && rs.Status != StatusSleep {
			at := r.LastActivity.Add(rs.EffectiveIdleTimeout)
			rs.SleepsAt = &at
		}
	}
	return rs
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		routes := s.store.GetAllRoutes()
		patched := s.patchedByRoute()
		result := make([]RouteStatus, 0, len(routes))
		for _, route := range routes {
			rs := s.routeStatus(route)
			rs.Resources = patched[route.ID]
			if rs.Resources == nil {
				rs.Resources = []patchedResource{}
			}
			result = append(result, rs)
		}
		writeJSON(w, result)

	case http.MethodPost:
		var route store.RouteConfig
		if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if route.Path == "" {
			route.Path = "/"
		}
		if route.Namespace == "" || route.Deployment == "" {
			http.Error(w, "Missing required fields", http.StatusBadRequest)
			return
		}
		if err := route.ValidateTraffic(); err != nil {
			http.Error(w, "Invalid traffic settings: "+err.Error(), http.StatusBadRequest)
			return
		}
		if route.Schedule != nil {
			if err := route.Schedule.Validate(); err != nil {
				http.Error(w, "Invalid schedule: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if s.k8sClient != nil && !s.k8sClient.Watches(route.Namespace) {
			http.Error(w, "Namespace "+route.Namespace+" is not managed by Smart Proxy", http.StatusForbidden)
			return
		}
		// A new route for a host served by an Ingress/Route in its namespace becomes bound to it.
		if route.ID == "" && s.k8sClient != nil {
			route.ID = s.resourceIDForHosts(route.Namespace, route.Host)
		}
		// Bound to an OpenShift Route balancing several Services: record them, managing the one
		// chosen as target and leaving the others alone.
		if kind, ns, name, ok := route.Resource(); ok && kind == store.KindRoute && len(route.Backends) == 0 && s.k8sClient != nil {
			if rt, err := s.k8sClient.GetRoute(ns, name); err == nil {
				route.Backends = s.k8sClient.RouteBackends(rt, []string{route.TargetService})
			}
		}
		if err := route.NormalizeBackends(); err != nil {
			http.Error(w, "Invalid backends: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := s.store.AddRoute(&route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Patch the Ingresses/Routes serving its hosts, and restore those of hosts it no longer has
		if s.k8sClient != nil {
			s.autoPatchResourcesForConfig(&route)
			s.releaseStale(route)
		}
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id", http.StatusBadRequest)
			return
		}
		if route, ok := s.store.GetRoute(id); ok && route.Declarative {
			http.Error(w, "This route is defined by smart-proxy/* annotations on its Ingress/Route: set smart-proxy/enabled to false there", http.StatusConflict)
			return
		}
		// Every resource patched for the route is restored: leaving them pointed at Smart Proxy
		// without a route would make them answer 404.
		restored, err := s.deleteRoute(id)
		if err != nil {
			http.Error(w, err.Error(), httpStatusFor(err))
			return
		}
		logger.Printf("Route %s deleted (%d resource(s) restored)", id, len(restored))
		if restored == nil {
			restored = []patchedResource{}
		}
		writeJSON(w, map[string]any{"restored": restored})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// resourceIDForHosts finds an Ingress or Route in the namespace serving one of the hosts
// (comma-separated) and returns the matching route ID, or "" if none does.
func (s *Server) resourceIDForHosts(namespace, hostList string) string {
	hosts := splitHosts(hostList)
	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			if rt.Namespace == namespace && containsFold(hosts, rt.Spec.Host) {
				return store.RouteID(rt.Namespace, rt.Name)
			}
		}
	}
	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			if ing.Namespace == namespace && len(ing.Spec.Rules) > 0 && containsFold(hosts, ing.Spec.Rules[0].Host) {
				return store.IngressID(ing.Namespace, ing.Name)
			}
		}
	}
	return ""
}

func splitHosts(hostList string) []string {
	var hosts []string
	for _, h := range strings.Split(hostList, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

func (s *Server) backendStates(r store.RouteConfig) []BackendState {
	states := make([]BackendState, 0, len(r.Backends))
	var serving int32
	for _, b := range r.Backends {
		st := BackendState{WeightedBackend: b, Status: "Unknown"}
		if b.Workload != "" {
			st.Status, _, _ = s.deploymentStatus(r.Namespace, b.Workload)
		}
		if b.Weight > 0 && (st.Status == StatusReady || st.Status == "Unknown") {
			serving += b.Weight
		}
		states = append(states, st)
	}
	for i := range states {
		if serving > 0 && states[i].Weight > 0 && (states[i].Status == StatusReady || states[i].Status == "Unknown") {
			states[i].Share = float64(states[i].Weight) * 100 / float64(serving)
		}
	}
	return states
}
