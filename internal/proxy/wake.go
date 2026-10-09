package proxy

import (
	"smart-proxy/internal/logger"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
)

// Workload states reported to the "waking up" page.
const (
	stateReady   = "Ready"   // At least one ready replica: can serve
	stateScaling = "Scaling" // Scaled up, no ready replica yet
	stateSleep   = "Sleep"   // At zero, waiting for its turn (start in order)
	stateError   = "Error"
)

type workloadState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ensureAwake checks every deployment the matched routes need and wakes those asleep.
// Routes with StartInOrder wake one dependency at a time, each once the previous serves,
// and their main deployment last; others wake everything at once. It reports each
// deployment's state and whether all of them can serve.
func (h *Handler) ensureAwake(routes []store.RouteConfig) ([]workloadState, bool) {
	var states []workloadState
	seen := map[string]string{} // "namespace/name" -> state
	allReady := true

	// check returns the deployment's state, waking it when allowed.
	check := func(namespace, name string, mayWake bool) string {
		key := namespace + "/" + name
		if state, ok := seen[key]; ok {
			return state
		}
		state := stateReady
		replicas, ready, err := h.k8sClient.GetDeploymentStatus(namespace, name)
		switch {
		case err != nil:
			logger.Printf("Error getting status for %s: %v", key, err)
			state = stateError
		case replicas == 0 && mayWake:
			if target, err := h.k8sClient.WakeDeployment(namespace, name); err != nil {
				logger.Printf("Error waking up %s: %v", key, err)
				state = stateError
			} else {
				if target > 0 {
					logger.Printf("Waking up %s with %d replica(s)", key, target)
					metrics.WakeStarted(namespace, name, "request")
				}
				state = stateScaling
			}
		case replicas == 0:
			state = stateSleep
		case ready == 0:
			state = stateScaling
		default:
			metrics.Ready(namespace, name)
		}
		seen[key] = state
		states = append(states, workloadState{Name: name, Status: state})
		return state
	}

	for _, route := range routes {
		chain := make([]string, 0, len(route.Dependencies)+1)
		for _, d := range route.Dependencies {
			chain = append(chain, d.Name)
		}
		chain = append(chain, route.Deployment)

		// Errors don't block the chain: the deployment may be gone, traffic decides.
		blocked := false
		for _, name := range chain {
			state := check(route.Namespace, name, !blocked)
			if state == stateScaling || state == stateSleep {
				allReady = false
				if route.StartInOrder {
					blocked = true
				}
			}
		}
	}
	return states, allReady
}
