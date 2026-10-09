package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// PatchableResource is an Ingress or OpenShift Route that can be put behind Smart Proxy.
type PatchableResource struct {
	Type      string `json:"type"` // "Ingress" or "Route"
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Host      string `json:"host"`
	Path      string `json:"path"`
	Service   string `json:"service"` // The application Service (the original one when patched)
	Port      string `json:"port"`
	Patched   bool   `json:"patched"`
	// Deployment behind the Service, when it can be resolved.
	Deployment *k8s.DeploymentSummary `json:"deployment"`
	// RouteID is the Smart Proxy route bound to this resource, if any.
	RouteID string `json:"route_id"`
	// Status is a short human-readable replica summary, e.g. "1/1 (Ready)".
	Status string `json:"status"`
}

func (s *Server) patchable(kind, namespace, name, host, path string, backend k8s.Backend, patched bool) PatchableResource {
	res := PatchableResource{
		Type: kind, Name: name, Namespace: namespace, Host: host, Path: path,
		Service: backend.Service, Port: backend.PortString(), Patched: patched, Status: "Unknown",
	}
	if dep, ok := s.k8sClient.DeploymentForBackend(namespace, backend.Service); ok {
		res.Deployment = dep
		res.Status = fmt.Sprintf("%d/%d", dep.Ready, dep.Replicas)
		switch {
		case dep.Replicas == 0:
			res.Status += " (Sleep)"
		case dep.Ready == dep.Replicas:
			res.Status += " (Ready)"
		default:
			res.Status += " (Not Ready)"
		}
	}
	for _, r := range s.store.GetAllRoutes() {
		if k, ns, n, ok := r.Resource(); ok && k == kind && ns == namespace && n == name {
			res.RouteID = r.ID
			break
		}
	}
	return res
}

func (s *Server) handleIngresses(w http.ResponseWriter, r *http.Request) {
	ings, err := s.k8sClient.ListIngresses()
	if err != nil {
		logger.Printf("Error listing ingresses: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res := []PatchableResource{}
	for _, ing := range ings {
		backend, ok := k8s.OriginalIngressBackend(ing)
		if !ok {
			continue // No Service backend to manage
		}
		res = append(res, s.patchable(store.KindIngress, ing.Namespace, ing.Name, k8s.IngressHost(ing), k8s.IngressPath(ing),
			backend, ing.Annotations[k8s.AnnotationPatched] == "true"))
	}
	writeJSON(w, res)
}

func (s *Server) handleOpenshiftRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.k8sClient.ListRoutes()
	if err != nil {
		logger.Printf("Error listing OpenShift routes: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res := []PatchableResource{}
	for _, rt := range routes {
		backend := k8s.Backend{Service: k8s.OriginalRouteService(rt)}
		if rt.Annotations[k8s.AnnotationPatched] == "true" {
			backend = k8s.Backend{Service: backend.Service, PortName: rt.Annotations[k8s.AnnotationOriginalPort]}
		} else if rt.Spec.Port != nil {
			backend.PortName = rt.Spec.Port.TargetPort.String()
		}
		res = append(res, s.patchable(store.KindRoute, rt.Namespace, rt.Name, k8s.RouteHost(rt), k8s.RoutePath(rt),
			backend, rt.Annotations[k8s.AnnotationPatched] == "true"))
	}
	writeJSON(w, res)
}

// resourceParams reads ?namespace=&name= for the patch endpoints.
func (s *Server) resourceParams(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return "", "", false
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Missing name", http.StatusBadRequest)
		return "", "", false
	}
	ns, ok := s.namespaceParam(w, r)
	return ns, name, ok
}

func httpStatusFor(err error) int {
	switch {
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsConflict(err):
		return http.StatusConflict
	case k8s.NotWatchedError(err):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

// newRouteConfig is the default configuration for a freshly patched resource.
func newRouteConfig(id, namespace, host, path, service string, port int, deployment string) *store.RouteConfig {
	return &store.RouteConfig{
		ID:            id,
		Host:          host,
		Path:          path,
		TargetService: service,
		TargetPort:    port,
		Namespace:     namespace,
		Deployment:    deployment,
		Dependencies:  []store.DependencyConfig{},
		IdleTimeout:   store.DefaultIdleTimeout,
		LastActivity:  time.Now(),
	}
}

func configJSON(config *store.RouteConfig) string {
	return config.AnnotationJSON()
}

// ingressPort resolves the numeric Service port an Ingress backend points to.
func (s *Server) ingressPort(namespace string, b k8s.Backend) int {
	if b.Port != 0 {
		return b.Port
	}
	port, err := s.k8sClient.ResolveServicePort(namespace, b.Service, &routev1.RoutePort{TargetPort: intstr.FromString(b.PortName)})
	if err != nil {
		return 80
	}
	return port
}

func (s *Server) handlePatchIngress(w http.ResponseWriter, r *http.Request) {
	ns, name, ok := s.resourceParams(w, r)
	if !ok {
		return
	}
	ing, err := s.k8sClient.GetIngress(ns, name)
	if err != nil {
		http.Error(w, err.Error(), httpStatusFor(err))
		return
	}
	if ing.Annotations[k8s.AnnotationPatched] == "true" {
		http.Error(w, "Already patched", http.StatusBadRequest)
		return
	}
	original, ok := k8s.IngressBackend(ing)
	if !ok {
		http.Error(w, k8s.ErrNoServiceBackend.Error(), http.StatusBadRequest)
		return
	}

	deployment, _ := s.k8sClient.ResolveDeploymentForService(ns, original.Service)
	config := newRouteConfig(store.IngressID(ns, name), ns, k8s.IngressHost(ing), k8s.IngressPath(ing),
		original.Service, s.ingressPort(ns, original), deployment)
	config.PathExact = k8s.IngressPathExact(ing)

	if err := k8s.PatchIngress(ing, s.ServiceName, original, configJSON(config)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The route first: an Ingress pointing at Smart Proxy without one would answer 404.
	if err := s.store.AddRoute(config); err != nil {
		http.Error(w, "Failed to save the route: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.k8sClient.UpdateIngress(ing); err != nil {
		if !apierrors.IsConflict(err) { // On a conflict, self-healing completes the patch
			s.store.RemoveRoute(config.ID)
		}
		http.Error(w, "Failed to update ingress: "+err.Error(), httpStatusFor(err))
		return
	}
	logger.Printf("Patched Ingress %s/%s (deployment %s)", ns, name, deployment)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUnpatchIngress(w http.ResponseWriter, r *http.Request) {
	ns, name, ok := s.resourceParams(w, r)
	if !ok {
		return
	}
	ing, err := s.k8sClient.GetIngress(ns, name)
	if err != nil {
		http.Error(w, err.Error(), httpStatusFor(err))
		return
	}
	if ing.Annotations[k8s.AnnotationDeclarative] == "true" {
		http.Error(w, "Patched because of its smart-proxy/enabled annotation: set it to false instead", http.StatusConflict)
		return
	}
	owner := ownerID(store.KindIngress, ns, name, ing.Annotations)
	res := patchedResource{ResourceRef{Kind: store.KindIngress, Namespace: ns, Name: name}, k8s.IngressHost(ing)}
	if err := k8s.UnpatchIngress(ing); err != nil {
		http.Error(w, "Not patched", http.StatusBadRequest)
		return
	}
	if err := s.k8sClient.UpdateIngress(ing); err != nil {
		http.Error(w, "Failed to update ingress: "+err.Error(), httpStatusFor(err))
		return
	}
	s.afterUnpatch(owner, res)
	logger.Printf("Restored Ingress %s/%s", ns, name)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePatchRoute(w http.ResponseWriter, r *http.Request) {
	ns, name, ok := s.resourceParams(w, r)
	if !ok {
		return
	}
	rt, err := s.k8sClient.GetRoute(ns, name)
	if err != nil {
		http.Error(w, err.Error(), httpStatusFor(err))
		return
	}
	if rt.Annotations[k8s.AnnotationPatched] == "true" {
		http.Error(w, "Already patched", http.StatusBadRequest)
		return
	}
	if err := k8s.RoutePatchable(rt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	original := s.routeOriginalBackend(rt)
	deployment, _ := s.k8sClient.ResolveDeploymentForService(ns, original.Service)
	config := newRouteConfig(store.RouteID(ns, name), ns, k8s.RouteHost(rt), k8s.RoutePath(rt),
		original.Service, original.Port, deployment)
	// Balanced across several Services: manage those running now, leave the others alone.
	config.Backends = s.k8sClient.RouteBackends(rt, nil)
	if err := config.NormalizeBackends(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	k8s.PatchRoute(rt, s.ServiceName, original, configJSON(config))
	// The route first: a Route pointing at Smart Proxy without one would answer 404.
	if err := s.store.AddRoute(config); err != nil {
		http.Error(w, "Failed to save the route: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.k8sClient.UpdateRoute(rt); err != nil {
		if !apierrors.IsConflict(err) { // On a conflict, self-healing completes the patch
			s.store.RemoveRoute(config.ID)
		}
		http.Error(w, "Failed to update route: "+err.Error(), httpStatusFor(err))
		return
	}
	logger.Printf("Patched Route %s/%s (deployment %s)", ns, name, deployment)
	w.WriteHeader(http.StatusOK)
}

// routeOriginalBackend is the Route's current Service with its target port resolved to a number.
func (s *Server) routeOriginalBackend(rt *routev1.Route) k8s.Backend {
	port, err := s.k8sClient.ResolveServicePort(rt.Namespace, rt.Spec.To.Name, rt.Spec.Port)
	if err != nil {
		port = 80
	}
	return k8s.Backend{Service: rt.Spec.To.Name, Port: port}
}

func (s *Server) handleUnpatchRoute(w http.ResponseWriter, r *http.Request) {
	ns, name, ok := s.resourceParams(w, r)
	if !ok {
		return
	}
	rt, err := s.k8sClient.GetRoute(ns, name)
	if err != nil {
		http.Error(w, err.Error(), httpStatusFor(err))
		return
	}
	if rt.Annotations[k8s.AnnotationDeclarative] == "true" {
		http.Error(w, "Patched because of its smart-proxy/enabled annotation: set it to false instead", http.StatusConflict)
		return
	}
	owner := ownerID(store.KindRoute, ns, name, rt.Annotations)
	res := patchedResource{ResourceRef{Kind: store.KindRoute, Namespace: ns, Name: name}, k8s.RouteHost(rt)}
	if err := k8s.UnpatchRoute(rt); err != nil {
		http.Error(w, "Not patched", http.StatusBadRequest)
		return
	}
	if err := s.k8sClient.UpdateRoute(rt); err != nil {
		http.Error(w, "Failed to update route: "+err.Error(), httpStatusFor(err))
		return
	}
	s.afterUnpatch(owner, res)
	logger.Printf("Restored Route %s/%s", ns, name)
	w.WriteHeader(http.StatusOK)
}

// autoPatchResourcesForConfig keeps the cluster in sync with a saved route: Ingresses and
// Routes in its namespace serving its hosts (or bound to it) get patched, and already patched
// ones get the new configuration annotation.
func (s *Server) autoPatchResourcesForConfig(config *store.RouteConfig) {
	hosts := splitHosts(config.Host)
	boundKind, boundNs, boundName, _ := config.Resource()
	annotation := configJSON(config)

	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			bound := boundKind == store.KindRoute && boundNs == rt.Namespace && boundName == rt.Name
			if rt.Namespace != config.Namespace || k8s.RoutePatchedByOther(rt, s.ServiceName) ||
				!(bound || (containsFold(hosts, k8s.RouteHost(rt)) && samePath(k8s.RoutePath(rt), config.Path))) {
				continue
			}
			if rt.Annotations[k8s.AnnotationPatched] == "true" {
				if rt.Annotations[k8s.AnnotationConfig] == annotation {
					continue
				}
				rt.Annotations[k8s.AnnotationConfig] = annotation
			} else {
				if err := k8s.RoutePatchable(rt); err != nil {
					logger.Printf("Not patching Route %s/%s for host %s: %v", rt.Namespace, rt.Name, k8s.RouteHost(rt), err)
					continue
				}
				logger.Printf("Auto-patching Route %s/%s for host %s", rt.Namespace, rt.Name, k8s.RouteHost(rt))
				k8s.PatchRoute(rt, s.ServiceName, s.routeOriginalBackend(rt), annotation)
			}
			if err := s.k8sClient.UpdateRoute(rt); err != nil {
				logger.Printf("Warning: Failed to update Route %s/%s: %v", rt.Namespace, rt.Name, err)
			}
		}
	}

	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			bound := boundKind == store.KindIngress && boundNs == ing.Namespace && boundName == ing.Name
			if ing.Namespace != config.Namespace || k8s.PatchedByOther(ing, s.ServiceName) ||
				!(bound || (containsFold(hosts, k8s.IngressHost(ing)) && samePath(k8s.IngressPath(ing), config.Path))) {
				continue
			}
			if ing.Annotations[k8s.AnnotationPatched] == "true" {
				if ing.Annotations[k8s.AnnotationConfig] == annotation {
					continue
				}
				ing.Annotations[k8s.AnnotationConfig] = annotation
			} else {
				original, ok := k8s.IngressBackend(ing)
				if !ok {
					continue
				}
				logger.Printf("Auto-patching Ingress %s/%s for host %s", ing.Namespace, ing.Name, k8s.IngressHost(ing))
				if err := k8s.PatchIngress(ing, s.ServiceName, original, annotation); err != nil {
					continue
				}
			}
			if err := s.k8sClient.UpdateIngress(ing); err != nil {
				logger.Printf("Warning: Failed to update Ingress %s/%s: %v", ing.Namespace, ing.Name, err)
			}
		}
	}
}

// SyncRoutesFromCluster loads the route configurations stored in the annotations of patched
// Ingresses and Routes, so they survive restarts without a persistent volume.
func (s *Server) SyncRoutesFromCluster() {
	count := 0
	load := func(kind, namespace, name, annotation string) {
		if annotation == "" {
			return
		}
		var config store.RouteConfig
		if err := json.Unmarshal([]byte(annotation), &config); err != nil {
			logger.Printf("Warning: invalid %s annotation on %s %s/%s: %v", k8s.AnnotationConfig, kind, namespace, name, err)
			return
		}
		if config.Namespace == "" {
			config.Namespace = namespace
		}
		if config.ID == "" {
			config.ID = store.IngressID(namespace, name)
			if kind == store.KindRoute {
				config.ID = store.RouteID(namespace, name)
			}
		}
		// The shared route store is the source of truth; annotations only fill gaps
		// (first start, or a store that was lost).
		if _, exists := s.store.GetRoute(config.ID); exists {
			return
		}
		if err := s.store.AddRoute(&config); err == nil {
			count++
		}
	}

	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			if !k8s.PatchedByOther(ing, s.ServiceName) {
				load(store.KindIngress, ing.Namespace, ing.Name, ing.Annotations[k8s.AnnotationConfig])
			}
		}
	} else {
		logger.Printf("Warning: Failed to list ingresses: %v", err)
	}
	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			if !k8s.RoutePatchedByOther(rt, s.ServiceName) {
				load(store.KindRoute, rt.Namespace, rt.Name, rt.Annotations[k8s.AnnotationConfig])
			}
		}
	}
	logger.Printf("Loaded %d route configuration(s) from Ingress/Route annotations", count)
}

// samePath compares Ingress/Route paths, "" being "/". Resources serving a route's host on
// another path belong to another application.
func samePath(a, b string) bool {
	norm := func(p string) string {
		if p == "" {
			return "/"
		}
		return p
	}
	return norm(a) == norm(b)
}
