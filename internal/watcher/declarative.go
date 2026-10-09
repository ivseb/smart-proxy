package watcher

import (
	"fmt"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"smart-proxy/internal/declarative"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// reconcileDeclarative applies the smart-proxy/* annotations users put on their Ingresses and
// Routes: opted-in resources are patched and their route configuration follows the annotations;
// resources that opt out again are restored.
func (w *Watcher) reconcileDeclarative() {
	if w.k8sClient == nil {
		return
	}
	if ings, err := w.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			w.reconcileIngress(ing)
		}
	}
	if routes, err := w.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			w.reconcileRoute(rt)
		}
	}
}

// declared builds the route configuration the annotations ask for, reusing the stored one
// (so its ID and activity stay) when the resource already has a route.
func (w *Watcher) declared(kind, namespace, name, host, path string, backend k8s.Backend, annotations map[string]string) (*store.RouteConfig, bool) {
	key := kind + " " + namespace + "/" + name
	settings, err := declarative.Parse(annotations)
	if err != nil {
		if w.invalid[key] != err.Error() {
			w.invalid[key] = err.Error()
			logger.Printf("Ignoring smart-proxy annotations on %s: %v", key, err)
		}
		return nil, false
	}
	delete(w.invalid, key)

	desired := &store.RouteConfig{
		ID:            store.IngressID(namespace, name),
		Namespace:     namespace,
		Host:          host,
		Path:          path,
		TargetService: backend.Service,
		TargetPort:    backend.Port,
		LastActivity:  time.Now(),
	}
	if kind == store.KindRoute {
		desired.ID = store.RouteID(namespace, name)
	}
	if existing := w.routeFor(kind, namespace, name); existing != nil {
		desired.ID = existing.ID // Possibly a legacy ID
		desired.LastActivity = existing.LastActivity
		desired.InspectUntil = existing.InspectUntil
	}
	desired.Deployment, _ = w.k8sClient.ResolveDeploymentForService(namespace, backend.Service)
	settings.Apply(desired) // smart-proxy/workload, when set, overrides the resolved workload
	desired.Declarative = true
	return desired, true
}

// reportInvalid logs why a resource's annotations can't be applied, once per reason.
func (w *Watcher) reportInvalid(key string, err error) {
	if w.invalid[key] != err.Error() {
		w.invalid[key] = err.Error()
		logger.Printf("Ignoring smart-proxy annotations on %s: %v", key, err)
	}
}

func (w *Watcher) routeFor(kind, namespace, name string) *store.RouteConfig {
	for _, r := range w.store.GetAllRoutes() {
		if k, ns, n, ok := r.Resource(); ok && k == kind && ns == namespace && n == name {
			return &r
		}
	}
	return nil
}

// save stores the configuration when it changed (LastActivity aside).
func (w *Watcher) save(desired *store.RouteConfig) {
	if existing, ok := w.store.GetRoute(desired.ID); ok && existing.AnnotationJSON() == desired.AnnotationJSON() {
		return
	}
	if err := w.store.AddRoute(desired); err != nil {
		logger.Printf("Error saving route %s from annotations: %v", desired.ID, err)
	}
}

func (w *Watcher) reconcileIngress(ing *networkingv1.Ingress) {
	if k8s.PatchedByOther(ing, w.serviceName) {
		return // Another Smart Proxy installation's
	}
	label := fmt.Sprintf("Ingress %s/%s", ing.Namespace, ing.Name)
	managed := ing.Annotations[k8s.AnnotationDeclarative] == "true"

	if !declarative.IsEnabled(ing.Annotations) {
		if managed {
			if err := k8s.UnpatchIngress(ing); err == nil {
				w.report(label+" (opted out, restored)", w.k8sClient.UpdateIngress(ing))
				w.store.RemoveResource(store.KindIngress, ing.Namespace, ing.Name)
			}
		}
		return
	}

	backend, ok := k8s.OriginalIngressBackend(ing)
	if !ok {
		return
	}
	if k8s.IsProxyService(backend.Service, w.serviceName) {
		return // Patched without a recorded original backend: self-healing deals with it
	}
	if backend.Port == 0 && backend.PortName != "" {
		if port, err := w.k8sClient.ResolveServicePort(ing.Namespace, backend.Service, &routev1.RoutePort{TargetPort: intstr.FromString(backend.PortName)}); err == nil {
			backend.Port = port
		}
	}
	desired, ok := w.declared(store.KindIngress, ing.Namespace, ing.Name, k8s.IngressHost(ing), k8s.IngressPath(ing), backend, ing.Annotations)
	if !ok {
		return
	}
	desired.PathExact = k8s.IngressPathExact(ing)
	w.save(desired)

	config := desired.AnnotationJSON()
	switch {
	case !k8s.IsIngressPatched(ing, w.serviceName):
		original, _ := k8s.OriginalIngressBackend(ing)
		if err := k8s.PatchIngress(ing, w.serviceName, original, config); err != nil {
			return
		}
		logger.Printf("Patching %s as asked by its smart-proxy/enabled annotation", label)
	case !managed || ing.Annotations[k8s.AnnotationConfig] != config:
		ing.Annotations[k8s.AnnotationConfig] = config
	default:
		return
	}
	ing.Annotations[k8s.AnnotationDeclarative] = "true"
	w.report(label, w.k8sClient.UpdateIngress(ing))
}

func (w *Watcher) reconcileRoute(rt *routev1.Route) {
	if k8s.RoutePatchedByOther(rt, w.serviceName) {
		return // Another Smart Proxy installation's
	}
	label := fmt.Sprintf("Route %s/%s", rt.Namespace, rt.Name)
	managed := rt.Annotations[k8s.AnnotationDeclarative] == "true"

	if !declarative.IsEnabled(rt.Annotations) {
		if managed {
			if err := k8s.UnpatchRoute(rt); err == nil {
				w.report(label+" (opted out, restored)", w.k8sClient.UpdateRoute(rt))
				w.store.RemoveResource(store.KindRoute, rt.Namespace, rt.Name)
			}
		}
		return
	}

	service := k8s.OriginalRouteService(rt)
	if k8s.IsProxyService(service, w.serviceName) {
		return
	}
	if err := k8s.RoutePatchable(rt); err != nil && !k8s.IsRoutePatched(rt, w.serviceName) {
		w.reportInvalid("Route "+rt.Namespace+"/"+rt.Name, err)
		return
	}
	backend := k8s.Backend{Service: service, Port: 80}
	if !k8s.IsRoutePatched(rt, w.serviceName) {
		if port, err := w.k8sClient.ResolveServicePort(rt.Namespace, service, rt.Spec.Port); err == nil {
			backend.Port = port
		}
	} else if existing := w.routeFor(store.KindRoute, rt.Namespace, rt.Name); existing != nil {
		backend.Port = existing.TargetPort
	}
	desired, ok := w.declared(store.KindRoute, rt.Namespace, rt.Name, k8s.RouteHost(rt), k8s.RoutePath(rt), backend, rt.Annotations)
	if !ok {
		return
	}
	// Balanced across Services: manage those the annotation lists, else keep the current choice,
	// else those running now.
	backends := traffic.SplitList(rt.Annotations[declarative.ManagedBackends])
	if len(backends) == 0 {
		if existing := w.routeFor(store.KindRoute, rt.Namespace, rt.Name); existing != nil {
			for _, b := range existing.Backends {
				if b.Managed {
					backends = append(backends, b.Service)
				}
			}
		}
	}
	if len(backends) == 0 {
		backends = nil
	}
	desired.Backends = w.k8sClient.RouteBackends(rt, backends)
	if err := desired.NormalizeBackends(); err != nil {
		logger.Printf("Ignoring smart-proxy annotations on Route %s/%s: %v", rt.Namespace, rt.Name, err)
		return
	}
	w.save(desired)

	config := desired.AnnotationJSON()
	switch {
	case !k8s.IsRoutePatched(rt, w.serviceName):
		k8s.PatchRoute(rt, w.serviceName, backend, config)
		logger.Printf("Patching %s as asked by its smart-proxy/enabled annotation", label)
	case !managed || rt.Annotations[k8s.AnnotationConfig] != config:
		rt.Annotations[k8s.AnnotationConfig] = config
	default:
		return
	}
	rt.Annotations[k8s.AnnotationDeclarative] = "true"
	w.report(label, w.k8sClient.UpdateRoute(rt))
}
