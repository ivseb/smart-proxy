package k8s

import (
	"strings"

	routev1 "github.com/openshift/api/route/v1"

	"smart-proxy/internal/store"
)

// RouteBackends returns the Services an OpenShift Route balances traffic across, or nil when it
// targets a single one. The services listed in managed are managed; without a list, those
// running now are (a backend kept off on purpose stays off), or the main one if none runs.
func (c *Client) RouteBackends(rt *routev1.Route, managed []string) []store.WeightedBackend {
	targets, port := OriginalRouteTargets(rt)
	if len(targets) < 2 {
		return nil
	}
	backends := make([]store.WeightedBackend, 0, len(targets))
	anyManaged := false
	for _, t := range targets {
		b := store.WeightedBackend{Service: t.Service, Weight: t.Weight, Port: 80}
		if p, err := c.ResolveServicePort(rt.Namespace, t.Service, port); err == nil {
			b.Port = p
		}
		running := false
		if ref, _ := c.ResolveDeploymentForService(rt.Namespace, t.Service); ref != "" {
			if replicas, _, err := c.GetDeploymentStatus(rt.Namespace, ref); err == nil {
				b.Workload, running = ref, replicas > 0
			}
		}
		if managed != nil {
			b.Managed = containsService(managed, t.Service)
		} else {
			b.Managed = running && t.Weight > 0
		}
		anyManaged = anyManaged || b.Managed
		backends = append(backends, b)
	}
	if !anyManaged {
		backends[0].Managed = true
	}
	return backends
}

func containsService(list []string, service string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == service {
			return true
		}
	}
	return false
}
