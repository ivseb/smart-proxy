package watcher

import (
	"context"

	"errors"
	"fmt"
	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"slices"
	"strings"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
)

type Watcher struct {
	k8sClient   *k8s.Client
	store       *store.Store
	serviceName string            // Service fronting Smart Proxy; patched Ingresses/Routes point at it
	kedaWarned  map[string]bool   // Deployments already reported as KEDA-managed (only touched by the watcher loop)
	invalid     map[string]string // Resource -> last reported annotation error, to log each error once
}

func NewWatcher(k8sClient *k8s.Client, store *store.Store, serviceName string) *Watcher {
	return &Watcher{
		k8sClient:   k8sClient,
		store:       store,
		serviceName: serviceName,
		kedaWarned:  make(map[string]bool),
		invalid:     make(map[string]string),
	}
}

// Start runs the idle and self-healing checks every 30s until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	logger.Println("Watcher started. Checking for idle services every 30s...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	tick := func() {
		w.reconcileDeclarative()
		w.checkIdleRoutes()
		w.healUnpatchedRoutes()
	}
	tick() // Don't make annotation changes wait a full interval after (re)gaining leadership
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

func (w *Watcher) checkIdleRoutes() {
	if w.k8sClient == nil {
		return
	}
	routes := w.store.GetAllRoutes()
	now := time.Now()

	// Scheduled hours: keep those routes (and their dependencies) awake.
	for _, route := range routes {
		if route.ScheduledAwake(now) && w.k8sClient.Watches(route.Namespace) {
			for _, workload := range route.ManagedWorkloads() {
				w.wake(route.Namespace, workload)
			}
			for _, dep := range route.Dependencies {
				w.wake(route.Namespace, dep.Name)
			}
		}
	}

	for _, route := range routes {
		// SAFETY: Only scale down if the route config represents a patched resource (Ingress or Route).
		// Manual configs (UUIDs) do not have traffic routed through smart-proxy, so scaling them down
		// would make them unreachable permanently.
		if _, _, _, ok := route.Resource(); !ok || !w.k8sClient.Watches(route.Namespace) {
			continue
		}
		if time.Since(route.LastActivity) <= route.EffectiveIdleTimeout() || route.ScheduledAwake(now) {
			continue
		}

		// Always On deployments and those still needed by another active route stay up.
		// (Not logged: this runs every tick for every idle route and would flood the log view.)
		// Only the route's own workloads: backends it doesn't manage are left as they are.
		// A workload woken less than an idle timeout ago stays up: the request that woke it may
		// have reached another replica, whose activity arrives here a little later.
		minAwake := route.EffectiveIdleTimeout()
		if !route.AlwaysOn {
			for _, workload := range route.ManagedWorkloads() {
				if !w.isDeploymentActive(routes, route.Namespace, workload) {
					w.sleep(route.Namespace, workload, minAwake, fmt.Sprintf("route %s%s idle since %s", route.Host, route.Path, route.LastActivity.Format(time.RFC3339)))
				}
			}
		}
		for _, dep := range route.Dependencies {
			if dep.StopOnIdle && !w.isDeploymentActive(routes, route.Namespace, dep.Name) {
				w.sleep(route.Namespace, dep.Name, minAwake, fmt.Sprintf("dependency of idle route %s%s", route.Host, route.Path))
			}
		}
	}
}

// sleep scales a deployment to zero if it is running, logging only when something happens.
func (w *Watcher) sleep(namespace, deployment string, minAwake time.Duration, reason string) {
	key := namespace + "/" + deployment
	slept, err := w.k8sClient.SleepIdleDeployment(namespace, deployment, minAwake)
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
		metrics.Slept(namespace, deployment, "idle")
	}
}

// wake scales a sleeping deployment up for its schedule.
func (w *Watcher) wake(namespace, deployment string) {
	target, err := w.k8sClient.WakeDeployment(namespace, deployment)
	if err != nil {
		logger.Printf("Error waking %s/%s for its schedule: %v", namespace, deployment, err)
		return
	}
	if target > 0 {
		logger.Printf("Woke %s/%s with %d replica(s): scheduled hours started", namespace, deployment, target)
		metrics.WakeStarted(namespace, deployment, "schedule")
	}
}

// isDeploymentActive checks if a workload is needed by a route that keeps it up: an active one
// (as its own workload or a dependency), an Always On one, or a manual one, which Smart Proxy
// never wakes.
func (w *Watcher) isDeploymentActive(routes []store.RouteConfig, namespace, workload string) bool {
	workload = canonical(workload)
	for _, r := range routes {
		if r.Namespace != namespace {
			continue
		}
		_, _, _, patched := r.Resource()
		active := time.Since(r.LastActivity) <= r.EffectiveIdleTimeout() || r.ScheduledAwake(time.Now())
		if (active || !patched || r.AlwaysOn) && slices.ContainsFunc(r.ManagedWorkloads(), func(x string) bool { return canonical(x) == workload }) {
			return true
		}
		if !active && patched {
			continue // An idle route's dependencies may sleep; an Always On route only keeps its own workloads
		}
		for _, dep := range r.Dependencies {
			if canonical(dep.Name) == workload {
				return true
			}
		}
	}
	return false
}

// canonical spells a workload reference one way ("deployment/web" and "web", "sts/db" and
// "statefulset/db" are the same workload).
func canonical(ref string) string { return k8s.WorkloadRef(k8s.ParseWorkload(ref)) }

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
				// The route's own Route, and others serving one of its hosts on the same path. A
				// Route for another path of the host is a different application.
				if rt.Namespace != ns || (rt.Name != name && !(containsFold(hosts, rt.Spec.Host) && samePath(k8s.RoutePath(rt), config.Path))) {
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
					if rt.Name == name {
						w.retarget(config, original)
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
			} else {
				target := original // Resolved for the proxy; the annotation keeps a named port as it is
				if target.Port == 0 && target.PortName != "" {
					if port, err := w.k8sClient.ResolveServicePort(ns, target.Service, &routev1.RoutePort{TargetPort: intstr.FromString(target.PortName)}); err == nil {
						target.Port = port
					}
				}
				w.retarget(config, target)
			}
			if err := k8s.PatchIngress(ing, w.serviceName, original, config.AnnotationJSON()); err != nil {
				continue
			}
			w.report(fmt.Sprintf("Ingress %s/%s", ns, name), w.k8sClient.UpdateIngress(ing))
		}
	}
}

// retarget follows an application whose Ingress/Route now points at another Service or port
// (e.g. after a Helm upgrade): the route forwards there, and wakes the workload behind it.
func (w *Watcher) retarget(config store.RouteConfig, backend k8s.Backend) {
	if backend.Service == "" || backend.Port == 0 || (backend.Service == config.TargetService && backend.Port == config.TargetPort) {
		return
	}
	if len(config.Backends) > 0 {
		return // Balanced routes are re-read with their Route when re-patched from the dashboard
	}
	updated := config
	updated.TargetService, updated.TargetPort = backend.Service, backend.Port
	if workload, err := w.k8sClient.ResolveDeploymentForService(config.Namespace, backend.Service); err == nil && workload != "" {
		updated.Deployment = workload
	}
	if err := w.store.AddRoute(&updated); err != nil {
		logger.Printf("Error updating route %s for its new target %s:%d: %v", config.ID, backend.Service, backend.Port, err)
		return
	}
	logger.Printf("Route %s now targets %s:%d (workload %s), as its resource does", config.ID, backend.Service, backend.Port, updated.Deployment)
}

// samePath compares Ingress/Route paths, "" being "/".
func samePath(a, b string) bool {
	norm := func(p string) string {
		if p == "" {
			return "/"
		}
		return p
	}
	return norm(a) == norm(b)
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
