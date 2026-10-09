package proxy

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net/http"

	"smart-proxy/internal/store"
)

// target is where a request is proxied.
type target struct {
	Service string
	Port    int
}

// backendServing reports whether a backend can take traffic now: its workload has a ready
// replica. A backend whose workload is unknown is assumed to serve, as the router would try it.
func (h *Handler) backendServing(namespace string, b store.WeightedBackend) bool {
	if b.Workload == "" {
		return true
	}
	replicas, ready, err := h.k8sClient.GetDeploymentStatus(namespace, b.Workload)
	if err != nil {
		return true
	}
	return replicas > 0 && ready > 0
}

// servingBackends are the route's backends that can take traffic now (weight above zero).
func (h *Handler) servingBackends(route store.RouteConfig) []store.WeightedBackend {
	var serving []store.WeightedBackend
	for _, b := range route.Backends {
		if b.Weight > 0 && h.backendServing(route.Namespace, b) {
			serving = append(serving, b)
		}
	}
	return serving
}

// hasServingPassThrough reports whether a backend Smart Proxy doesn't manage is running, so it
// can take the traffic while the managed ones wake up.
func (h *Handler) hasServingPassThrough(route store.RouteConfig) bool {
	for _, b := range h.servingBackends(route) {
		if !b.Managed {
			return true
		}
	}
	return false
}

// stickyCookie keeps a client on the backend it was given, as the OpenShift router does.
func stickyCookie(routeID string) string {
	h := fnv.New32a()
	h.Write([]byte(routeID))
	return fmt.Sprintf("sp_backend_%08x", h.Sum32())
}

// pickTarget chooses the Service for a request: the route's single target, or one of its
// serving backends, by weight, sticking to the backend chosen before for this client.
func (h *Handler) pickTarget(w http.ResponseWriter, r *http.Request, route store.RouteConfig) target {
	if len(route.Backends) == 0 {
		return target{route.TargetService, route.TargetPort}
	}
	serving := h.servingBackends(route)
	if len(serving) == 0 {
		return target{route.TargetService, route.TargetPort}
	}

	name := stickyCookie(route.ID)
	if c, err := r.Cookie(name); err == nil {
		for _, b := range serving {
			if b.Service == c.Value {
				return target{b.Service, b.Port}
			}
		}
	}

	var total int64
	for _, b := range serving {
		total += int64(b.Weight)
	}
	chosen := serving[0]
	n := rand.Int64N(total)
	for _, b := range serving {
		if n < int64(b.Weight) {
			chosen = b
			break
		}
		n -= int64(b.Weight)
	}
	// While a managed backend wakes up, its share goes elsewhere for now: don't stick to that.
	for _, b := range route.Backends {
		if b.Managed && b.Weight > 0 && !h.backendServing(route.Namespace, b) {
			return target{chosen.Service, chosen.Port}
		}
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: name, Value: chosen.Service, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	return target{chosen.Service, chosen.Port}
}
