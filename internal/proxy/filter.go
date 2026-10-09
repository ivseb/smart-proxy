package proxy

import (
	"fmt"
	"net"
	"net/http"

	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// ignoredReason says why a request doesn't count as activity ("" when it does): it matches the
// global or the route's rules, or is a monitor calling one of the workloads' Kubernetes probe
// paths (e.g. /healthz through the ingress). Browsers and the root path always count: many apps
// probe "/", their home page.
func (h *Handler) ignoredReason(r *http.Request, client net.IP, route store.RouteConfig) string {
	rules := h.GlobalRules
	if route.Ignore != nil {
		rules = rules.Merge(*route.Ignore)
	}
	if reason, ok := rules.Match(r, client); ok {
		return reason
	}
	if r.URL.Path == "/" || traffic.ClientName(r.UserAgent()) == traffic.Browser {
		return ""
	}
	for _, workload := range append([]string{route.Deployment}, dependencyNames(route)...) {
		paths, err := h.k8sClient.GetDeploymentProbePaths(route.Namespace, workload)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if r.URL.Path == p {
				return traffic.ReasonProbePath
			}
		}
	}
	return ""
}

func dependencyNames(route store.RouteConfig) []string {
	names := make([]string, 0, len(route.Dependencies))
	for _, d := range route.Dependencies {
		names = append(names, d.Name)
	}
	return names
}

// serving reports whether the route can take a request without waking anything: its workload,
// or one of its backends, has a ready replica.
func (h *Handler) serving(route store.RouteConfig) bool {
	if len(route.Backends) > 0 {
		return len(h.servingBackends(route)) > 0
	}
	replicas, ready, err := h.k8sClient.GetDeploymentStatus(route.Namespace, route.Deployment)
	return err == nil && replicas > 0 && ready > 0
}

// answerAsleep replies to an ignored request for a sleeping route, according to its WhenAsleep
// setting. It reports false when the route should be woken instead.
func (h *Handler) answerAsleep(w http.ResponseWriter, route store.RouteConfig) bool {
	status := http.StatusOK
	switch route.WhenAsleep {
	case store.WhenAsleepWake:
		return false
	case store.WhenAsleepUnavailable:
		status = http.StatusServiceUnavailable
		w.Header().Set("Retry-After", "60")
	}
	metrics.AsleepResponse(route.Namespace, route.ID, status)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Smart-Proxy", "asleep")
	w.WriteHeader(status)
	fmt.Fprintf(w, "%s is asleep (Smart Proxy); it wakes up on the next visit.\n", route.Deployment)
	return true
}
