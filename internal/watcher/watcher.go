package watcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

type Watcher struct {
	k8sClient   *k8s.Client
	store       *store.Store
	serviceName string          // Service fronting Smart Proxy; patched Ingresses/Routes point at it
	kedaWarned  map[string]bool // Deployments already reported as KEDA-managed (only touched by the watcher loop)
}

func NewWatcher(k8sClient *k8s.Client, store *store.Store, serviceName string) *Watcher {
	return &Watcher{
		k8sClient:   k8sClient,
		store:       store,
		serviceName: serviceName,
		kedaWarned:  make(map[string]bool),
	}
}

// Start runs the idle and self-healing checks every 30s until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	logger.Println("Watcher started. Checking for idle services every 30s...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkIdleRoutes()
			w.healUnpatchedRoutes()
		}
	}
}

func (w *Watcher) checkIdleRoutes() {
	if w.k8sClient == nil {
		return
	}
	routes := w.store.GetAllRoutes()

	for _, route := range routes {
		// SAFETY: Only scale down if the route config represents a patched resource (Ingress or Route).
		// Manual configs (UUIDs) do not have traffic routed through smart-proxy, so scaling them down
		// would make them unreachable permanently.
		if _, _, _, ok := route.Resource(); !ok || !w.k8sClient.Watches(route.Namespace) {
			continue
		}
		if time.Since(route.LastActivity) <= route.EffectiveIdleTimeout() {
			continue
		}

		// Always On deployments and those still needed by another active route stay up.
		// (Not logged: this runs every tick for every idle route and would flood the log view.)
		if !route.AlwaysOn && !w.isDeploymentActive(routes, route.Namespace, route.Deployment) {
			w.sleep(route.Namespace, route.Deployment, fmt.Sprintf("route %s%s idle since %s", route.Host, route.Path, route.LastActivity.Format(time.RFC3339)))
		}
		for _, dep := range route.Dependencies {
			if dep.StopOnIdle && !w.isDeploymentActive(routes, route.Namespace, dep.Name) {
				w.sleep(route.Namespace, dep.Name, fmt.Sprintf("dependency of idle route %s%s", route.Host, route.Path))
			}
		}
	}
}

// sleep scales a deployment to zero if it is running, logging only when something happens.
func (w *Watcher) sleep(namespace, deployment, reason string) {
	key := namespace + "/" + deployment
	slept, err := w.k8sClient.SleepDeployment(namespace, deployment)
	switch {
	case errors.Is(err, k8s.ErrManagedByKEDA):
		if !w.kedaWarned[key] {
			w.kedaWarned[key] = true
			logger.Printf("Not putting %s to sleep: its %v, which would scale it back up. Use KEDA's own scale-to-zero, or remove the ScaledObject.", key, err)
		}
	case err != nil:
		logger.Printf("Error putting %s to sleep: %v", key, err)
	case slept:
		logger.Printf("Scaled down %s (%s)", key, reason)
	}
}

// isDeploymentActive checks if a deployment is needed by any active route (either as main deployment or dependency)
func (w *Watcher) isDeploymentActive(routes []store.RouteConfig, namespace, deploymentName string) bool {
	for _, r := range routes {
		if r.Namespace != namespace || time.Since(r.LastActivity) > r.EffectiveIdleTimeout() {
			continue
		}
		if r.Deployment == deploymentName {
			return true
		}
		for _, dep := range r.Dependencies {
			if dep.Name == deploymentName {
				return true
			}
		}
	}
	return false
}

// healUnpatchedRoutes re-applies the patch to Ingresses/Routes that were reverted by someone
// else (typically a Helm upgrade or a GitOps sync of the application).
func (w *Watcher) healUnpatchedRoutes() {
	if w.k8sClient == nil {
		return
	}
	var routes []store.RouteConfig
	for _, r := range w.store.GetAllRoutes() {
		if kind, ns, _, ok := r.Resource(); ok && w.k8sClient.Watches(ns) && (kind == store.KindIngress || w.k8sClient.RoutesEnabled()) {
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		return
	}

	osRoutes, err := w.k8sClient.ListRoutes()
	if err != nil {
		logger.Printf("Self-Healing Warning: Failed to list OpenShift routes: %v", err)
	}

	for _, config := range routes {
		kind, ns, name, _ := config.Resource()
		switch kind {
		case store.KindRoute:
			hosts := strings.Split(config.Host, ",")
			for _, rt := range osRoutes {
				if rt.Namespace != ns || (rt.Name != name && !containsFold(hosts, rt.Spec.Host)) {
					continue
				}
				if k8s.IsRoutePatched(rt, w.serviceName) {
					continue
				}
				logger.Printf("Self-Healing: Route %s/%s has been unpatched (likely by Helm). Re-applying patch...", rt.Namespace, rt.Name)
				original := k8s.Backend{Service: config.TargetService, Port: config.TargetPort}
				if !k8s.IsProxyService(rt.Spec.To.Name, w.serviceName) {
					// The spec holds the application again (e.g. re-deployed by Helm): it is the source of truth.
					original.Service = rt.Spec.To.Name
					if port, err := w.k8sClient.ResolveServicePort(rt.Namespace, rt.Spec.To.Name, rt.Spec.Port); err == nil {
						original.Port = port
					}
				}
				k8s.PatchRoute(rt, w.serviceName, original, config.AnnotationJSON())
				w.report(fmt.Sprintf("Route %s/%s", rt.Namespace, rt.Name), w.k8sClient.UpdateRoute(rt))
			}

		case store.KindIngress:
			ing, err := w.k8sClient.GetIngress(ns, name)
			if err != nil || k8s.IsIngressPatched(ing, w.serviceName) {
				continue
			}
			original, ok := k8s.IngressBackend(ing)
			if !ok {
				continue
			}
			logger.Printf("Self-Healing: Ingress %s/%s has been unpatched. Re-applying patch...", ns, name)
			if k8s.IsProxyService(original.Service, w.serviceName) {
				// Still pointing at us, so the backend no longer holds the original target.
				// (Also migrates Ingresses patched by older versions with a numeric port.)
				original = k8s.Backend{Service: config.TargetService, Port: config.TargetPort}
			}
			if err := k8s.PatchIngress(ing, w.serviceName, original, config.AnnotationJSON()); err != nil {
				continue
			}
			w.report(fmt.Sprintf("Ingress %s/%s", ns, name), w.k8sClient.UpdateIngress(ing))
		}
	}
}

func (w *Watcher) report(resource string, err error) {
	if err != nil {
		logger.Printf("Self-Healing Warning: Failed to re-apply patch to %s: %v", resource, err)
	} else {
		logger.Printf("Self-Healing Success: Re-applied patch to %s", resource)
	}
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), s) {
			return true
		}
	}
	return false
}
