// Package admin implements the administrative HTTP server for the Smart Proxy.
// It provides API endpoints for managing routes, viewing logs, and interacting with Kubernetes resources.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"

	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Server represents the admin HTTP server.
type Server struct {
	k8sClient *k8s.Client
	store     *store.Store
	Metrics   *proxy.Metrics
	ProxyPort int
}

// NewServer creates a new instance of the admin Server.
// It initializes the server with the provided Kubernetes client, configuration store, and metrics collector.
// It also reads the SMART_PROXY_PORT environment variable to configure the proxy port (default: 80).
func NewServer(k8sClient *k8s.Client, store *store.Store, metrics *proxy.Metrics) *Server {
	portStr := os.Getenv("SMART_PROXY_PORT")
	port := 80
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	return &Server{
		k8sClient: k8sClient,
		store:     store,
		Metrics:   metrics,
		ProxyPort: port,
	}
}

// ListenAndServe starts the admin server on the specified address.
// It performs an initial sync of routes from Ingresses and then blocks while serving HTTP requests.
func (s *Server) ListenAndServe(addr string) error {
	// Sync Routes from Ingresses on startup
	if s.k8sClient != nil {
		go s.SyncRoutesFromIngresses()
	}

	mux := http.NewServeMux()

	// Static Files (Admin UI)
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/", fs)

	// API Endpoints
	mux.HandleFunc("/api/routes", s.handleRoutes)
	mux.HandleFunc("/api/k8s/namespaces", s.handleNamespaces)
	mux.HandleFunc("/api/k8s/deployments", s.handleDeployments)
	mux.HandleFunc("/api/k8s/ingresses", s.handleIngresses)
	mux.HandleFunc("/api/k8s/routes", s.handleOpenshiftRoutes) // New
	mux.HandleFunc("/api/patch-ingress", s.handlePatchIngress)
	mux.HandleFunc("/api/unpatch-ingress", s.handleUnpatchIngress)
	mux.HandleFunc("/api/patch-route", s.handlePatchRoute)     // New
	mux.HandleFunc("/api/unpatch-route", s.handleUnpatchRoute) // New
	mux.HandleFunc("/api/stats", s.handleStats)
	// New Endpoints
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/k8s/stop-deployment", s.handleStopDeployment)
	mux.HandleFunc("/api/k8s/deployment-service-info", s.handleDeploymentServiceInfo)
	mux.HandleFunc("/api/k8s/service-routes", s.handleServiceRoutes)

	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	// SSE Handler
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientChan := logger.Get().Subscribe()
	defer logger.Get().Unsubscribe(clientChan)

	// Send history first
	history := logger.Get().GetHistory()
	for _, entry := range history {
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

func (s *Server) handleStopDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	deployment := r.URL.Query().Get("deployment")

	if namespace == "" || deployment == "" {
		http.Error(w, "Missing namespace or deployment", http.StatusBadRequest)
		return
	}

	if s.k8sClient != nil {
		err := s.k8sClient.ScaleDeployment(namespace, deployment, 0)
		if err != nil {
			logger.Printf("Error scaling down %s: %v", deployment, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logger.Printf("Manual shutdown triggered for %s/%s", namespace, deployment)

		// Stop dependencies if configured
		routes := s.store.GetAllRoutes()
		for _, r := range routes {
			if r.Namespace == namespace && r.Deployment == deployment {
				for _, dep := range r.Dependencies {
					if dep.StopOnIdle {
						logger.Printf("Stopping dependency %s for manual stop of %s", dep.Name, deployment)
						// We ignore error here to ensure we try others
						if err := s.k8sClient.ScaleDeployment(namespace, dep.Name, 0); err != nil {
							logger.Printf("Error stopping dependency %s: %v", dep.Name, err)
						}
					}
				}
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.Metrics != nil {
		json.NewEncoder(w).Encode(s.Metrics)
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		routes := s.store.GetAllRoutes()

		// Enrich with Status
		type RouteStatus struct {
			store.RouteConfig
			Status           string            `json:"status"`            // "Ready", "Scaling", "Sleep", "Error"
			DependencyStatus map[string]string `json:"dependency_status"` // DepName -> Status
		}

		enrichedRoutes := make([]RouteStatus, 0, len(routes))

		for _, r := range routes {
			// Get Main Status
			status := "Unknown"
			if s.k8sClient == nil {
				status = "K8s Client Unavailable"
			} else {
				replicas, ready, err := s.k8sClient.GetDeploymentStatus(r.Namespace, r.Deployment)
				if err != nil {
					status = "Error"
				} else if replicas == 0 {
					status = "Sleep"
				} else if ready < replicas {
					status = "Scaling"
				} else {
					status = "Ready"
				}
			}

			// Get Dependency Status
			depStatus := make(map[string]string)
			if s.k8sClient == nil {
				for _, dep := range r.Dependencies {
					depStatus[dep.Name] = "K8s Client Unavailable"
				}
			} else {
				for _, dep := range r.Dependencies {
					dReplicas, dReady, err := s.k8sClient.GetDeploymentStatus(r.Namespace, dep.Name)
					if err != nil {
						depStatus[dep.Name] = "Error"
					} else if dReplicas == 0 {
						depStatus[dep.Name] = "Sleep"
					} else if dReady < dReplicas {
						depStatus[dep.Name] = "Scaling"
					} else {
						depStatus[dep.Name] = "Ready"
					}
				}
			}

			enrichedRoutes = append(enrichedRoutes, RouteStatus{
				RouteConfig:      r,
				Status:           status,
				DependencyStatus: depStatus,
			})
		}

		json.NewEncoder(w).Encode(enrichedRoutes)
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

		// If ID is empty, check if we can resolve it from a matching Route/Ingress in the cluster
		if route.ID == "" && s.k8sClient != nil {
			configHosts := strings.Split(route.Host, ",")
			for i := range configHosts {
				configHosts[i] = strings.TrimSpace(configHosts[i])
			}

			// Try matching Routes first
			routesList, err := s.k8sClient.ListRoutes()
			if err == nil {
				for _, rt := range routesList {
					for _, h := range configHosts {
						if strings.EqualFold(h, rt.Spec.Host) {
							route.ID = "route-" + rt.Name
							break
						}
					}
					if route.ID != "" {
						break
					}
				}
			}

			// Try matching Ingresses if still empty
			if route.ID == "" {
				ingsList, err := s.k8sClient.ListIngresses()
				if err == nil {
					for _, ing := range ingsList {
						if len(ing.Spec.Rules) > 0 {
							for _, h := range configHosts {
								if strings.EqualFold(h, ing.Spec.Rules[0].Host) {
									route.ID = "ing-" + ing.Name
									break
								}
							}
						}
						if route.ID != "" {
							break
						}
					}
				}
			}
		}

		if err := s.store.AddRoute(&route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Auto-patch any matching routes/ingresses in the cluster to keep in sync
		if s.k8sClient != nil {
			s.autoPatchResourcesForConfig(&route)
		}

		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing id", http.StatusBadRequest)
			return
		}
		if err := s.store.RemoveRoute(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.k8sClient == nil {
		logger.Println("K8s Client is nil, returning mock namespaces")
		json.NewEncoder(w).Encode([]string{"default", "kube-system", "my-app-ns"})
		return
	}

	namespaces, err := s.k8sClient.ListNamespaces()
	if err != nil {
		logger.Printf("Error listing namespaces: %v. Returning mock data.", err)
		json.NewEncoder(w).Encode([]string{"default", "kube-system", "my-app-ns"})
		return
	}
	json.NewEncoder(w).Encode(namespaces)
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		http.Error(w, "Missing namespace", http.StatusBadRequest)
		return
	}

	if s.k8sClient == nil {
		log.Println("K8s Client is nil, returning mock deployments")
		json.NewEncoder(w).Encode([]string{"nginx", "frontend", "backend"})
		return
	}

	deployments, err := s.k8sClient.ListDeployments("") // Env var in client handles the NS
	if err != nil {
		logger.Printf("Error listing deployments: %v. Returning mock data.", err)
		json.NewEncoder(w).Encode([]string{"nginx", "frontend", "backend"})
		return
	}
	json.NewEncoder(w).Encode(deployments)
}

func (s *Server) handleIngresses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.k8sClient == nil {
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}

	ings, err := s.k8sClient.ListIngresses()
	if err != nil {
		logger.Printf("Error listing ingresses: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := context.TODO()
	svcList, err := s.k8sClient.Clientset.CoreV1().Services(s.k8sClient.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Printf("Debug: Failed to list services: %v", err)
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}
	depList, err := s.k8sClient.Clientset.AppsV1().Deployments(s.k8sClient.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Printf("Debug: Failed to list deployments: %v", err)
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}

	depsMap := make(map[string]*appsv1.Deployment)
	for i := range depList.Items {
		dep := &depList.Items[i]
		depsMap[dep.Name] = dep
	}

	svcsMap := make(map[string]*corev1.Service)
	for i := range svcList.Items {
		svc := &svcList.Items[i]
		svcsMap[svc.Name] = svc
	}

	resolveDeploymentStatus := func(svcName string) string {
		var matchedDepName string
		found := false

		if strings.HasSuffix(svcName, "-svc") {
			stripped := strings.TrimSuffix(svcName, "-svc")
			if dep, ok := depsMap[stripped]; ok {
				matchedDepName = dep.Name
				found = true
			}
		}

		if !found {
			if dep, ok := depsMap[svcName]; ok {
				matchedDepName = dep.Name
				found = true
			}
		}

		if !found {
			if svc, ok := svcsMap[svcName]; ok && len(svc.Spec.Selector) > 0 {
				for _, dep := range depList.Items {
					match := true
					for k, v := range svc.Spec.Selector {
						if depVal, ok := dep.Spec.Selector.MatchLabels[k]; !ok || depVal != v {
							match = false
							break
						}
					}
					if match {
						matchedDepName = dep.Name
						found = true
						break
					}
				}
			}
		}

		if found {
			if dep, ok := depsMap[matchedDepName]; ok {
				replicas := int32(0)
				if dep.Spec.Replicas != nil {
					replicas = *dep.Spec.Replicas
				}
				ready := dep.Status.ReadyReplicas
				statusStr := fmt.Sprintf("%d/%d", ready, replicas)
				if replicas == 0 {
					statusStr += " (Sleep)"
				} else if ready == replicas {
					statusStr += " (Ready)"
				} else {
					statusStr += " (Not Ready)"
				}
				return statusStr
			}
		}

		return "Unknown"
	}

	var res []PatchableResource
	for _, ing := range ings {
		host := ""
		if len(ing.Spec.Rules) > 0 {
			host = ing.Spec.Rules[0].Host
		}
		patched := ing.Annotations["smart-proxy/patched"] == "true"

		targetSvc := ""
		targetPort := 80
		if patched {
			targetSvc = ing.Annotations["smart-proxy/original-service"]
		} else {
			if len(ing.Spec.Rules) > 0 && len(ing.Spec.Rules[0].HTTP.Paths) > 0 {
				targetSvc = ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name
				targetPort = int(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)
			}
		}

		statusStr := "Unknown"
		if targetSvc != "" {
			statusStr = resolveDeploymentStatus(targetSvc)
		}

		res = append(res, PatchableResource{
			Name:      ing.Name,
			Namespace: ing.Namespace,
			Host:      host,
			Service:   targetSvc,
			Port:      targetPort,
			Patched:   patched,
			Status:    statusStr,
			Type:      "Ingress",
		})
	}
	json.NewEncoder(w).Encode(res)
}

func (s *Server) handlePatchIngress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Missing name", http.StatusBadRequest)
		return
	}

	ing, err := s.k8sClient.GetIngress(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if ing.Annotations == nil {
		ing.Annotations = make(map[string]string)
	}
	if ing.Annotations["smart-proxy/patched"] == "true" {
		http.Error(w, "Already patched", http.StatusBadRequest)
		return
	}

	// Assume first rule, first path for simplicity V2.5
	if len(ing.Spec.Rules) == 0 || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
		http.Error(w, "Ingress has no rules", http.StatusBadRequest)
		return
	}
	rule := ing.Spec.Rules[0]
	path := rule.HTTP.Paths[0]

	originalSvc := path.Backend.Service.Name
	originalPort := int(path.Backend.Service.Port.Number)

	depName, err := s.k8sClient.ResolveDeploymentForService(originalSvc)
	if err != nil {
		depName = originalSvc
	}

	// Save original info
	ing.Annotations["smart-proxy/patched"] = "true"
	ing.Annotations["smart-proxy/original-service"] = originalSvc
	ing.Annotations["smart-proxy/original-port"] = strconv.Itoa(originalPort)

	// Update Ingress to point to Us
	path.Backend.Service.Name = "smart-proxy"
	path.Backend.Service.Port.Number = int32(s.ProxyPort)
	ing.Spec.Rules[0].HTTP.Paths[0] = path

	routeConfig := &store.RouteConfig{
		ID:            "ing-" + name,
		Host:          rule.Host,
		Path:          path.Path,
		TargetService: originalSvc,
		TargetPort:    originalPort,
		Namespace:     ing.Namespace,
		Deployment:    depName,
		Dependencies:  []store.DependencyConfig{},
		IdleTimeout:   30 * 60 * 1000 * 1000 * 1000,
		LastActivity:  time.Now(),
	}

	// Persist Config to Annotation
	configBytes, _ := json.Marshal(routeConfig)
	ing.Annotations["smart-proxy/config"] = string(configBytes)

	// Update Ingress with both patch and config
	if err := s.k8sClient.UpdateIngress(ing); err != nil {
		http.Error(w, "Failed to update ingress: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Add Route to Store
	err = s.store.AddRoute(routeConfig)
	if err != nil {
		logger.Printf("Warning: Failed to add route to store: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUnpatchIngress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")

	ing, err := s.k8sClient.GetIngress(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if ing.Annotations["smart-proxy/patched"] != "true" {
		http.Error(w, "Not patched", http.StatusBadRequest)
		return
	}

	originalSvc := ing.Annotations["smart-proxy/original-service"]

	// Restore
	if len(ing.Spec.Rules) > 0 && len(ing.Spec.Rules[0].HTTP.Paths) > 0 {
		ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = originalSvc
		ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = 80 // Hardcoded for demo
	}

	delete(ing.Annotations, "smart-proxy/patched")
	delete(ing.Annotations, "smart-proxy/original-service")

	if err := s.k8sClient.UpdateIngress(ing); err != nil {
		http.Error(w, "Failed to update ingress: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.store.RemoveRoute("ing-" + name)
	w.WriteHeader(http.StatusOK)
}

type PatchableResource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Host      string `json:"host"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	Patched   bool   `json:"patched"`
	Status    string `json:"status"`
	Type      string `json:"type"` // "Ingress" or "Route"
}

// OpenShift Route Handlers

func (s *Server) handleOpenshiftRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.k8sClient == nil {
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}
	routes, err := s.k8sClient.ListRoutes()
	if err != nil {
		logger.Printf("Debug: Failed to list OpenShift routes: %v", err)
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}

	ctx := context.TODO()
	svcList, err := s.k8sClient.Clientset.CoreV1().Services(s.k8sClient.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Printf("Debug: Failed to list services: %v", err)
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}
	depList, err := s.k8sClient.Clientset.AppsV1().Deployments(s.k8sClient.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Printf("Debug: Failed to list deployments: %v", err)
		json.NewEncoder(w).Encode([]PatchableResource{})
		return
	}

	depsMap := make(map[string]*appsv1.Deployment)
	for i := range depList.Items {
		dep := &depList.Items[i]
		depsMap[dep.Name] = dep
	}

	svcsMap := make(map[string]*corev1.Service)
	for i := range svcList.Items {
		svc := &svcList.Items[i]
		svcsMap[svc.Name] = svc
	}

	resolveDeploymentStatus := func(svcName string) string {
		var matchedDepName string
		found := false

		if strings.HasSuffix(svcName, "-svc") {
			stripped := strings.TrimSuffix(svcName, "-svc")
			if dep, ok := depsMap[stripped]; ok {
				matchedDepName = dep.Name
				found = true
			}
		}

		if !found {
			if dep, ok := depsMap[svcName]; ok {
				matchedDepName = dep.Name
				found = true
			}
		}

		if !found {
			if svc, ok := svcsMap[svcName]; ok && len(svc.Spec.Selector) > 0 {
				for _, dep := range depList.Items {
					match := true
					for k, v := range svc.Spec.Selector {
						if depVal, ok := dep.Spec.Selector.MatchLabels[k]; !ok || depVal != v {
							match = false
							break
						}
					}
					if match {
						matchedDepName = dep.Name
						found = true
						break
					}
				}
			}
		}

		if found {
			if dep, ok := depsMap[matchedDepName]; ok {
				replicas := int32(0)
				if dep.Spec.Replicas != nil {
					replicas = *dep.Spec.Replicas
				}
				ready := dep.Status.ReadyReplicas
				statusStr := fmt.Sprintf("%d/%d", ready, replicas)
				if replicas == 0 {
					statusStr += " (Sleep)"
				} else if ready == replicas {
					statusStr += " (Ready)"
				} else {
					statusStr += " (Not Ready)"
				}
				return statusStr
			}
		}

		return "Unknown"
	}

	var res []PatchableResource
	for _, route := range routes {
		host := route.Spec.Host
		patched := route.Annotations["smart-proxy/patched"] == "true"
		targetSvc := ""
		if patched {
			targetSvc = route.Annotations["smart-proxy/original-service"]
		} else {
			targetSvc = route.Spec.To.Name
		}

		statusStr := "Unknown"
		if targetSvc != "" {
			statusStr = resolveDeploymentStatus(targetSvc)
		}

		res = append(res, PatchableResource{
			Name:      route.Name,
			Namespace: route.Namespace,
			Host:      host,
			Service:   targetSvc,
			Port:      80, // Assumption
			Patched:   patched,
			Status:    statusStr,
			Type:      "Route",
		})
	}
	json.NewEncoder(w).Encode(res)
}

func (s *Server) handlePatchRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")

	route, err := s.k8sClient.GetRoute(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if route.Annotations == nil {
		route.Annotations = make(map[string]string)
	}
	if route.Annotations["smart-proxy/patched"] == "true" {
		http.Error(w, "Already patched", http.StatusBadRequest)
		return
	}

	// Route Target Port check
	originalSvc := route.Spec.To.Name
	targetPort, err := s.k8sClient.ResolveServicePort(originalSvc, route.Spec.Port)
	if err != nil {
		targetPort = 80 // fallback
	}

	depName, err := s.k8sClient.ResolveDeploymentForService(originalSvc)
	if err != nil {
		depName = originalSvc
	}

	// Save original info
	route.Annotations["smart-proxy/patched"] = "true"
	route.Annotations["smart-proxy/original-service"] = originalSvc
	route.Annotations["smart-proxy/original-port"] = strconv.Itoa(targetPort)

	// Save original to weight and alternate backends
	type originalBackends struct {
		ToWeight          *int32                         `json:"toWeight,omitempty"`
		AlternateBackends []routev1.RouteTargetReference `json:"alternateBackends,omitempty"`
	}
	origBackends := originalBackends{
		ToWeight:          route.Spec.To.Weight,
		AlternateBackends: route.Spec.AlternateBackends,
	}
	if origBackendsBytes, err := json.Marshal(origBackends); err == nil {
		route.Annotations["smart-proxy/original-backends"] = string(origBackendsBytes)
	}

	// Update Route to point to Us
	route.Spec.To.Name = "smart-proxy"
	route.Spec.To.Weight = nil
	route.Spec.AlternateBackends = nil

	// Set target port to ProxyPort (admin/proxy port)
	if route.Spec.Port == nil {
		route.Spec.Port = &routev1.RoutePort{}
	}
	route.Spec.Port.TargetPort = intstr.FromString("proxy")

	routePath := route.Spec.Path
	if routePath == "" {
		routePath = "/"
	}

	routeConfig := &store.RouteConfig{
		ID:            "route-" + name, // Convention for Routes
		Host:          route.Spec.Host,
		Path:          routePath,
		TargetService: originalSvc,
		TargetPort:    targetPort,
		Namespace:    route.Namespace,
		Deployment:   depName,
		Dependencies: []store.DependencyConfig{},
		IdleTimeout:  30 * 60 * 1000 * 1000 * 1000,
		LastActivity: time.Now(),
	}

	// Persist Config
	configBytes, _ := json.Marshal(routeConfig)
	route.Annotations["smart-proxy/config"] = string(configBytes)

	if err := s.k8sClient.UpdateRoute(route); err != nil {
		http.Error(w, "Failed to update route: "+err.Error(), http.StatusInternalServerError)
		return
	}

	err = s.store.AddRoute(routeConfig)
	if err != nil {
		logger.Printf("Warning: Failed to add route to store: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUnpatchRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")

	route, err := s.k8sClient.GetRoute(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if route.Annotations["smart-proxy/patched"] != "true" {
		http.Error(w, "Not patched", http.StatusBadRequest)
		return
	}

	originalSvc := route.Annotations["smart-proxy/original-service"]
	originalPortStr := route.Annotations["smart-proxy/original-port"]

	// Restore
	// Restore
	route.Spec.To.Name = originalSvc
	if originalPortStr != "" {
		if portNum, err := strconv.Atoi(originalPortStr); err == nil {
			route.Spec.Port.TargetPort = intstr.FromInt(portNum)
		} else {
			route.Spec.Port.TargetPort = intstr.FromString(originalPortStr)
		}
	} else {
		route.Spec.Port.TargetPort = intstr.IntOrString{}
	}

	origBackendsStr := route.Annotations["smart-proxy/original-backends"]
	if origBackendsStr != "" {
		type originalBackends struct {
			ToWeight          *int32                         `json:"toWeight,omitempty"`
			AlternateBackends []routev1.RouteTargetReference `json:"alternateBackends,omitempty"`
		}
		var origBackends originalBackends
		if err := json.Unmarshal([]byte(origBackendsStr), &origBackends); err == nil {
			route.Spec.To.Weight = origBackends.ToWeight
			route.Spec.AlternateBackends = origBackends.AlternateBackends
		}
	} else {
		// Fallback for legacy patched routes
		route.Spec.To.Weight = nil
		route.Spec.AlternateBackends = nil
	}

	delete(route.Annotations, "smart-proxy/patched")
	delete(route.Annotations, "smart-proxy/original-service")
	delete(route.Annotations, "smart-proxy/original-port")
	delete(route.Annotations, "smart-proxy/original-backends")
	delete(route.Annotations, "smart-proxy/config")

	if err := s.k8sClient.UpdateRoute(route); err != nil {
		http.Error(w, "Failed to update route: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.store.RemoveRoute("route-" + name)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) SyncRoutesFromIngresses() {
	logger.Println("Syncing routes from existing Ingresses and Routes...")
	if s.k8sClient == nil {
		return
	}
	// Ingresses
	ings, err := s.k8sClient.ListIngresses()
	if err != nil {
		logger.Printf("Warning: Failed to list ingresses: %v", err)
	} else {
		count := 0
		for _, ing := range ings {
			configJSON := ing.Annotations["smart-proxy/config"]
			if configJSON != "" {
				var config store.RouteConfig
				if err := json.Unmarshal([]byte(configJSON), &config); err == nil {
					if config.ID == "" {
						config.ID = "ing-" + ing.Name
					}
					s.store.AddRoute(&config)
					count++
				}
			}
		}
		logger.Printf("Synced %d routes from Ingresses", count)
	}

	// Routes
	routes, err := s.k8sClient.ListRoutes()
	if err != nil {
		// Log debug only, failure expected on non-OCP
		// logger.Printf("Debug: Failed to list routes: %v", err)
	} else {
		count := 0
		for _, route := range routes {
			configJSON := route.Annotations["smart-proxy/config"]
			if configJSON != "" {
				var config store.RouteConfig
				if err := json.Unmarshal([]byte(configJSON), &config); err == nil {
					if config.ID == "" {
						config.ID = "route-" + route.Name
					}
					s.store.AddRoute(&config)
					count++
				}
			}
		}
		logger.Printf("Synced %d routes from OpenShift Routes", count)
	}
}

func (s *Server) handleDeploymentServiceInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	dep := r.URL.Query().Get("deployment")
	if dep == "" {
		http.Error(w, "Missing deployment", http.StatusBadRequest)
		return
	}
	if s.k8sClient == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service": "",
			"port":    80,
		})
		return
	}

	svcName, port, err := s.k8sClient.ResolveServiceForDeployment(dep)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service": "",
			"port":    80,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"service": svcName,
		"port":    port,
	})
}

func (s *Server) handleServiceRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	svc := r.URL.Query().Get("service")
	if svc == "" {
		http.Error(w, "Missing service", http.StatusBadRequest)
		return
	}

	type RouteInfo struct {
		Name string `json:"name"`
		Host string `json:"host"`
	}

	var matchingRoutes []RouteInfo
	if s.k8sClient != nil {
		routes, err := s.k8sClient.ListRoutes()
		if err == nil {
			for _, rt := range routes {
				targetSvc := rt.Spec.To.Name
				if rt.Annotations["smart-proxy/patched"] == "true" {
					targetSvc = rt.Annotations["smart-proxy/original-service"]
				}
				if targetSvc == svc {
					matchingRoutes = append(matchingRoutes, RouteInfo{
						Name: rt.Name,
						Host: rt.Spec.Host,
					})
				}
			}
		}
	}

	json.NewEncoder(w).Encode(matchingRoutes)
}

func (s *Server) autoPatchResourcesForConfig(config *store.RouteConfig) {
	s.autoPatchRoutesForConfig(config)
	s.autoPatchIngressesForConfig(config)
}

func (s *Server) autoPatchRoutesForConfig(config *store.RouteConfig) {
	if s.k8sClient == nil {
		return
	}
	routes, err := s.k8sClient.ListRoutes()
	if err != nil {
		return
	}

	configHosts := strings.Split(config.Host, ",")
	for i := range configHosts {
		configHosts[i] = strings.TrimSpace(configHosts[i])
	}

	primaryRouteName := ""
	if len(config.ID) > 6 && config.ID[:6] == "route-" {
		primaryRouteName = config.ID[6:]
	}

	for _, rt := range routes {
		isPrimary := rt.Name == primaryRouteName

		// Check if this route's host is in the config's hosts
		hostMatch := false
		for _, h := range configHosts {
			if strings.EqualFold(h, rt.Spec.Host) {
				hostMatch = true
				break
			}
		}

		if isPrimary || hostMatch {
			// Check if it's already patched
			if rt.Annotations["smart-proxy/patched"] == "true" {
				configBytes, _ := json.Marshal(config)
				if rt.Annotations["smart-proxy/config"] != string(configBytes) {
					if rt.Annotations == nil {
						rt.Annotations = make(map[string]string)
					}
					rt.Annotations["smart-proxy/config"] = string(configBytes)
					if err := s.k8sClient.UpdateRoute(rt); err != nil {
						logger.Printf("Warning: Failed to update config annotation on already-patched route %s: %v", rt.Name, err)
					} else {
						logger.Printf("Updated config annotation on route %s", rt.Name)
					}
				}
				continue
			}

			// Not patched, let's patch it!
			logger.Printf("Auto-patching route %s because it is associated with config", rt.Name)

			originalSvc := rt.Spec.To.Name
			targetPort, err := s.k8sClient.ResolveServicePort(originalSvc, rt.Spec.Port)
			if err != nil {
				targetPort = 80
			}

			if rt.Annotations == nil {
				rt.Annotations = make(map[string]string)
			}
			rt.Annotations["smart-proxy/patched"] = "true"
			rt.Annotations["smart-proxy/original-service"] = originalSvc
			rt.Annotations["smart-proxy/original-port"] = strconv.Itoa(targetPort)

			// Save original to weight and alternate backends
			type originalBackends struct {
				ToWeight          *int32                         `json:"toWeight,omitempty"`
				AlternateBackends []routev1.RouteTargetReference `json:"alternateBackends,omitempty"`
			}
			origBackends := originalBackends{
				ToWeight:          rt.Spec.To.Weight,
				AlternateBackends: rt.Spec.AlternateBackends,
			}
			if origBackendsBytes, err := json.Marshal(origBackends); err == nil {
				rt.Annotations["smart-proxy/original-backends"] = string(origBackendsBytes)
			}

			// Update Route to point to Us
			rt.Spec.To.Name = "smart-proxy"
			rt.Spec.To.Weight = nil
			rt.Spec.AlternateBackends = nil

			if rt.Spec.Port == nil {
				rt.Spec.Port = &routev1.RoutePort{}
			}
			rt.Spec.Port.TargetPort = intstr.FromString("proxy")

			// Persist this config to the route
			configBytes, _ := json.Marshal(config)
			rt.Annotations["smart-proxy/config"] = string(configBytes)

			if err := s.k8sClient.UpdateRoute(rt); err != nil {
				logger.Printf("Warning: Failed to auto-patch route %s: %v", rt.Name, err)
			} else {
				logger.Printf("Successfully auto-patched route %s", rt.Name)
			}
		}
	}
}

func (s *Server) autoPatchIngressesForConfig(config *store.RouteConfig) {
	if s.k8sClient == nil {
		return
	}
	ings, err := s.k8sClient.ListIngresses()
	if err != nil {
		return
	}

	configHosts := strings.Split(config.Host, ",")
	for i := range configHosts {
		configHosts[i] = strings.TrimSpace(configHosts[i])
	}

	primaryIngressName := ""
	if len(config.ID) > 4 && config.ID[:4] == "ing-" {
		primaryIngressName = config.ID[4:]
	}

	for _, ing := range ings {
		if len(ing.Spec.Rules) == 0 || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
			continue
		}
		rule := ing.Spec.Rules[0]
		isPrimary := ing.Name == primaryIngressName

		hostMatch := false
		for _, h := range configHosts {
			if strings.EqualFold(h, rule.Host) {
				hostMatch = true
				break
			}
		}

		if isPrimary || hostMatch {
			if ing.Annotations["smart-proxy/patched"] == "true" {
				configBytes, _ := json.Marshal(config)
				if ing.Annotations["smart-proxy/config"] != string(configBytes) {
					if ing.Annotations == nil {
						ing.Annotations = make(map[string]string)
					}
					ing.Annotations["smart-proxy/config"] = string(configBytes)
					if err := s.k8sClient.UpdateIngress(ing); err != nil {
						logger.Printf("Warning: Failed to update config annotation on already-patched ingress %s: %v", ing.Name, err)
					} else {
						logger.Printf("Updated config annotation on ingress %s", ing.Name)
					}
				}
				continue
			}

			logger.Printf("Auto-patching ingress %s because it is associated with config", ing.Name)

			path := rule.HTTP.Paths[0]
			originalSvc := path.Backend.Service.Name
			originalPort := int(path.Backend.Service.Port.Number)

			if ing.Annotations == nil {
				ing.Annotations = make(map[string]string)
			}
			ing.Annotations["smart-proxy/patched"] = "true"
			ing.Annotations["smart-proxy/original-service"] = originalSvc
			ing.Annotations["smart-proxy/original-port"] = strconv.Itoa(originalPort)

			path.Backend.Service.Name = "smart-proxy"
			path.Backend.Service.Port.Number = int32(s.ProxyPort)
			ing.Spec.Rules[0].HTTP.Paths[0] = path

			configBytes, _ := json.Marshal(config)
			ing.Annotations["smart-proxy/config"] = string(configBytes)

			if err := s.k8sClient.UpdateIngress(ing); err != nil {
				logger.Printf("Warning: Failed to auto-patch ingress %s: %v", ing.Name, err)
			} else {
				logger.Printf("Successfully auto-patched ingress %s", ing.Name)
			}
		}
	}
}
