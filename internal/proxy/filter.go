package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// ignoredReason says why a request doesn't count as activity ("" when it does), and the rule
// that matched: the route's rules, the global ones, or, for a monitor, one of the workloads'
// Kubernetes probe paths (e.g. /healthz through the ingress). Browsers and the root path always
// count: many apps probe "/", their home page.
func (h *Handler) ignoredReason(r *http.Request, client net.IP, route store.RouteConfig) (string, string) {
	if route.Ignore != nil {
		if reason, rule, ok := route.Ignore.MatchRule(r, client); ok {
			return reason, rule
		}
	}
	if reason, rule, ok := h.GlobalRules.MatchRule(r, client); ok {
		return reason, rule
	}
	if r.URL.Path == "/" || traffic.ClientName(r.UserAgent()) == traffic.Browser {
		return "", ""
	}
	for _, workload := range append([]string{route.Deployment}, dependencyNames(route)...) {
		paths, err := h.k8sClient.GetDeploymentProbePaths(route.Namespace, workload)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if r.URL.Path == p {
				return traffic.ReasonProbePath, p
			}
		}
	}
	return "", ""
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

// answerBlocked tells a request that the route's application can't be woken now (its blocked
// hours), and when it can again.
func (h *Handler) answerBlocked(w http.ResponseWriter, route store.RouteConfig) {
	metrics.WakeBlocked(route.Namespace, route.ID)
	msg := "Smart Proxy: the application is not available at this time (blocked hours)"
	if st := h.Blackout.Get().State(route, time.Now()); st.Change != nil {
		w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(*st.Change).Seconds())+1))
		msg += "; it can be started again from " + st.Change.Format(time.RFC3339)
	}
	w.Header().Set("X-Smart-Proxy", "blocked")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, http.StatusServiceUnavailable)
}
