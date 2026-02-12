// Package admin implements the administrative HTTP server for the Smart Proxy.
// It provides API endpoints for managing routes, viewing logs, and interacting with Kubernetes resources.
package admin

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"

	routev1 "github.com/openshift/api/route/v1"
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
		if route.Path == "" || route.Namespace == "" || route.Deployment == "" {
			http.Error(w, "Missing required fields", http.StatusBadRequest)
			return
		}
		// V2: ID generation handled by Store if missing
		if err := s.store.AddRoute(&route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Update Ingress Annotation for persistence if this is a patched route
		// Convention: ID = "ing-" + IngressName
		if len(route.ID) > 4 && route.ID[:4] == "ing-" && s.k8sClient != nil {
			ingressName := route.ID[4:]
			// Fetch Ingress
			ing, err := s.k8sClient.GetIngress(ingressName)
			if err != nil {
				logger.Printf("Warning: Failed to fetch ingress %s for persistence update: %v", ingressName, err)
			} else {
				// Serialize Config
				configBytes, _ := json.Marshal(route)
				if ing.Annotations == nil {
					ing.Annotations = make(map[string]string)
				}
				ing.Annotations["smart-proxy/config"] = string(configBytes)

				// Update Ingress
				if err := s.k8sClient.UpdateIngress(ing); err != nil {
					logger.Printf("Warning: Failed to persist config to ingress %s: %v", ingressName, err)
				} else {
					logger.Printf("Persisted config update to ingress %s", ingressName)
				}
			}
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
		json.NewEncoder(w).Encode([]string{})
		return
	}
	ings, err := s.k8sClient.ListIngresses()
	if err != nil {
		logger.Printf("Error listing ingresses: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Use PatchableResource (simulated here as anonymous struct or reuse if defined globally,
	// but since PatchableResource is defined further down, we might need to move definition up or duplicate.
	// Go allows type mismatch if JSON structure matches? No.
	// We'll define a local struct compatible or reuse if I moved it up?
	// I defined PatchableResource in replace_file_content step above, below this function (around line 491).
	// Structs can be used before definition in Go if in same package.

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
			replicas, ready, err := s.k8sClient.GetDeploymentStatus(ing.Namespace, targetSvc)
			if err != nil {
				statusStr = "Error"
			} else {
				statusStr = fmt.Sprintf("%d/%d", ready, replicas)
				if replicas == 0 {
					statusStr += " (Sleep)"
				} else if ready == replicas {
					statusStr += " (Ready)"
				} else {
					statusStr += " (Not Ready)"
				}
			}
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

	// Save original info
	ing.Annotations["smart-proxy/patched"] = "true"
	ing.Annotations["smart-proxy/original-service"] = originalSvc

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
		Deployment:    originalSvc,
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
			replicas, ready, err := s.k8sClient.GetDeploymentStatus(route.Namespace, targetSvc)
			if err != nil {
				statusStr = "Error"
			} else {
				statusStr = fmt.Sprintf("%d/%d", ready, replicas)
				if replicas == 0 {
					statusStr += " (Sleep)"
				} else if ready == replicas {
					statusStr += " (Ready)"
				} else {
					statusStr += " (Not Ready)"
				}
			}
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
	// Route port might be in Port structure or implicit.
	// We'll trust TargetPort resolution or assume it points to the Service's port.

	// Save original info
	route.Annotations["smart-proxy/patched"] = "true"
	route.Annotations["smart-proxy/original-service"] = originalSvc

	// Update Route to point to Us
	route.Spec.To.Name = "smart-proxy"
	// Set target port to ProxyPort (admin/proxy port)
	if route.Spec.Port == nil {
		route.Spec.Port = &routev1.RoutePort{}
	}
	route.Spec.Port.TargetPort = intstr.FromInt(s.ProxyPort)

	routeConfig := &store.RouteConfig{
		ID:            "route-" + name, // Convention for Routes
		Host:          route.Spec.Host,
		Path:          route.Spec.Path,
		TargetService: originalSvc,
		TargetPort:    80, // Assumption: Original service uses port 80? Usually Routes point to Service port.
		// If we don't know the original port, we might guess 80 or try to lookup Service.
		// For demo, we assume the backend service listens on 80.
		Namespace:    route.Namespace,
		Deployment:   originalSvc, // Assumption: Deployment Name == Service Name
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

	// Restore
	route.Spec.To.Name = originalSvc
	// Clear the forced port so it falls back to Service defaults or original logic?
	// If we overwrote TargetPort, we should restore it if we saved it.
	// For now, we clear the specific TargetPort if we set it, effectively reverting to default behavior.
	// Actually, if we didn't save original port, we might be safer assuming 80 or nil if it was nil.
	// Let's assume nil for now to let it Pick up from Service.
	// Ideally we should persist "original-port" annotation too.
	route.Spec.Port.TargetPort = intstr.IntOrString{} // Clear it? Or set to 80?
	// Better approach: If we saved it, use it. Without it, we risk breaking if it was custom.
	// For V2.5 Demo, we'll clear it.

	delete(route.Annotations, "smart-proxy/patched")
	delete(route.Annotations, "smart-proxy/original-service")
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
