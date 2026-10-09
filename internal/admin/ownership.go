package admin

import (
	"context"
	"encoding/json"

	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"strings"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
	"smart-proxy/internal/vault"
)

// A route can patch several Ingresses/Routes (one per host). Each patched resource records its
// route in the smart-proxy/config annotation, which is how they are found again.

// patchedResource is an Ingress or Route patched on behalf of a route.
type patchedResource struct {
	ResourceRef
	Host string `json:"host"`
}

// ownerID returns the route a patched resource belongs to, from its config annotation.
func ownerID(kind, namespace, name string, annotations map[string]string) string {
	if annotations[k8s.AnnotationPatched] != "true" {
		return ""
	}
	var config store.RouteConfig
	if json.Unmarshal([]byte(annotations[k8s.AnnotationConfig]), &config) == nil && config.ID != "" {
		return config.ID
	}
	// Patched before config annotations carried an ID: the resource's own route.
	if kind == store.KindRoute {
		return store.RouteID(namespace, name)
	}
	return store.IngressID(namespace, name)
}

// patchedByRoute maps route IDs to the resources currently patched for them. Legacy IDs
// ("ing-<name>") are resolved to the resource they name.
func (s *Server) patchedByRoute() map[string][]patchedResource {
	result := map[string][]patchedResource{}
	if s.k8sClient == nil {
		return result
	}
	legacy := map[string]string{} // namespaced ID of a resource -> stored legacy ID
	for _, r := range s.store.GetAllRoutes() {
		if kind, ns, name, ok := r.Resource(); ok {
			id := store.IngressID(ns, name)
			if kind == store.KindRoute {
				id = store.RouteID(ns, name)
			}
			if id != r.ID {
				legacy[id] = r.ID
			}
		}
	}
	add := func(kind, ns, name, host string, annotations map[string]string) {
		id := ownerID(kind, ns, name, annotations)
		if id == "" {
			return
		}
		if l, ok := legacy[id]; ok {
			id = l
		}
		result[id] = append(result[id], patchedResource{ResourceRef{Kind: kind, Namespace: ns, Name: name}, host})
	}
	if ings, err := s.k8sClient.ListIngresses(); err == nil {
		for _, ing := range ings {
			add(store.KindIngress, ing.Namespace, ing.Name, k8s.IngressHost(ing), ing.Annotations)
		}
	}
	if routes, err := s.k8sClient.ListRoutes(); err == nil {
		for _, rt := range routes {
			add(store.KindRoute, rt.Namespace, rt.Name, k8s.RouteHost(rt), rt.Annotations)
		}
	}
	return result
}

// unpatch restores one resource's original backend.
func (s *Server) unpatch(res ResourceRef) error {
	switch res.Kind {
	case store.KindRoute:
		rt, err := s.k8sClient.GetRoute(res.Namespace, res.Name)
		if err != nil {
			return err
		}
		if err := k8s.UnpatchRoute(rt); err != nil {
			return nil // Not patched (anymore)
		}
		return s.k8sClient.UpdateRoute(rt)
	default:
		ing, err := s.k8sClient.GetIngress(res.Namespace, res.Name)
		if err != nil {
			return err
		}
		if err := k8s.UnpatchIngress(ing); err != nil {
			return nil
		}
		return s.k8sClient.UpdateIngress(ing)
	}
}

// deleteRoute removes a route and restores every resource patched for it.
func (s *Server) deleteRoute(id string) ([]patchedResource, error) {
	route, ok := s.store.GetRoute(id)
	if !ok {
		return nil, nil
	}
	var restored []patchedResource
	if kind, ns, name, bound := route.Resource(); s.k8sClient != nil && bound && !s.k8sClient.Watches(ns) {
		// Outside the watched namespaces (not in the caches): restore through the API directly.
		owns := func(o k8s.RouteOwner) bool {
			return o.Patched && (o.ID == route.ID || (o.ID == "" && o.Kind == kind && o.Name == name))
		}
		workloads := route.ManagedWorkloads()
		for _, d := range route.Dependencies {
			workloads = append(workloads, d.Name)
		}
		if _, err := s.k8sClient.ReleaseNamespace(ns, s.ServiceName, owns, workloads); err != nil {
			if !apierrors.IsForbidden(err) && !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("restoring route %s in unwatched namespace %s: %w", id, ns, err)
			}
			// No permission there anymore (or no namespace): the route goes anyway, or it could
			// never be deleted. Whatever is left needs restoring by hand.
			logger.Printf("Warning: route %s deleted without restoring namespace %s (%v); restore its patched Ingresses/Routes and sleeping workloads by hand if any", id, ns, err)
		}
	} else if s.k8sClient != nil {
		for _, res := range s.patchedByRoute()[id] {
			if err := s.unpatch(res.ResourceRef); err != nil {
				return restored, fmt.Errorf("restoring %s %s/%s: %w", res.Kind, res.Namespace, res.Name, err)
			}
			logger.Printf("Restored %s %s/%s (route %s deleted)", res.Kind, res.Namespace, res.Name, id)
			restored = append(restored, res)
		}
	}
	if err := s.store.RemoveRoute(route.ID); err != nil {
		return restored, err
	}
	s.forgetCredentials(route.ID)
	return restored, nil
}

// forgetCredentials removes the users and tokens of a deleted route.
func (s *Server) forgetCredentials(id string) {
	if v := s.vault(); v.Ready() && (len(v.Credentials(id).Users) > 0 || len(v.Credentials(id).Tokens) > 0) {
		if err := v.UpdateCredentials(context.Background(), id, func(c *vault.Credentials) { *c = vault.Credentials{} }); err != nil {
			logger.Printf("Warning: removing the credentials of route %s: %v", id, err)
		}
	}
}

// moveCredentials keeps a route's users and tokens when its ID changes.
func (s *Server) moveCredentials(from, to string) {
	v := s.vault()
	creds := v.Credentials(from)
	if !v.Ready() || (len(creds.Users) == 0 && len(creds.Tokens) == 0) {
		return
	}
	if err := v.UpdateCredentials(context.Background(), to, func(c *vault.Credentials) { *c = creds }); err != nil {
		logger.Printf("Warning: moving the credentials of route %s to %s: %v", from, to, err)
		return
	}
	s.forgetCredentials(from)
}

// releaseStale restores resources a route patched for hosts it no longer has (after an edit).
func (s *Server) releaseStale(route store.RouteConfig) {
	_, boundNs, boundName, _ := route.Resource()
	hosts := splitHosts(route.Host)
	for _, res := range s.patchedByRoute()[route.ID] {
		bound := res.Namespace == boundNs && res.Name == boundName
		if bound || containsFold(hosts, res.Host) {
			continue
		}
		if err := s.unpatch(res.ResourceRef); err != nil {
			logger.Printf("Warning: failed to restore %s %s/%s: %v", res.Kind, res.Namespace, res.Name, err)
			continue
		}
		logger.Printf("Restored %s %s/%s: host %s was removed from route %s", res.Kind, res.Namespace, res.Name, res.Host, route.ID)
	}
}

// afterUnpatch updates the route owning a resource that was just unpatched by hand: its host
// is dropped (or self-healing would patch it again), the route is re-bound to another of its
// resources if it was bound to this one, and removed when nothing is left.
func (s *Server) afterUnpatch(owner string, res patchedResource) {
	route, ok := s.store.GetRoute(owner)
	if !ok {
		return
	}
	var remaining []patchedResource
	for _, other := range s.patchedByRoute()[owner] {
		if other.ResourceRef != res.ResourceRef {
			remaining = append(remaining, other)
		}
	}
	if len(remaining) == 0 {
		s.store.RemoveRoute(owner)
		s.forgetCredentials(owner)
		logger.Printf("Route %s removed: its last patched resource was restored", owner)
		return
	}

	// Its host leaves the route, unless another of its resources still serves it. A route bound
	// to resources never ends up without hosts: that would make it catch every host.
	stillServed := func(host string) bool {
		for _, other := range remaining {
			if strings.EqualFold(other.Host, host) {
				return true
			}
		}
		return false
	}
	var hosts []string
	for _, h := range splitHosts(route.Host) {
		if !strings.EqualFold(h, res.Host) || stillServed(h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		for _, other := range remaining {
			if other.Host != "" && !containsFold(hosts, other.Host) {
				hosts = append(hosts, other.Host)
			}
		}
	}
	updated := *route
	updated.Host = strings.Join(hosts, ", ")
	if _, ns, name, _ := route.Resource(); ns == res.Namespace && name == res.Name {
		next := remaining[0]
		updated.ID = store.IngressID(next.Namespace, next.Name)
		if next.Kind == store.KindRoute {
			updated.ID = store.RouteID(next.Namespace, next.Name)
		}
	}
	if err := s.store.AddRoute(&updated); err != nil {
		logger.Printf("Warning: failed to update route %s: %v", owner, err)
		return
	}
	if updated.ID != owner {
		s.moveCredentials(owner, updated.ID)
		s.store.RemoveRoute(owner)
		logger.Printf("Route %s is now %s", owner, updated.ID)
	}
	s.autoPatchResourcesForConfig(&updated) // Refresh the config annotation of the remaining resources
}
