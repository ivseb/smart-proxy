package watcher

import (
	"time"

	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// startupLimit is how long a woken stack may take to get ready. Until then it isn't put to sleep,
// and its idle timeout starts once it serves: a slow start doesn't eat into it. A stack that
// never gets ready (crash loop, missing image) is then treated as usual.
const startupLimit = 30 * time.Minute

// starting reports whether the route's stack (dependencies and own workloads), woken by Smart
// Proxy, is still starting. When it gets ready its idle timeout starts.
func (w *Watcher) starting(route store.RouteConfig, now time.Time) bool {
	chain := make([]string, 0, len(route.Dependencies)+1)
	for _, d := range route.Dependencies {
		chain = append(chain, d.Name)
	}
	own := route.WakeWorkloads()
	chain = append(chain, own...)

	var woken time.Time
	up, awake := true, false
	for i, name := range chain {
		if t, ok := w.k8sClient.WokenAt(route.Namespace, name); ok && t.After(woken) {
			woken = t
		}
		replicas, ready, err := w.k8sClient.GetDeploymentStatus(route.Namespace, name)
		if err != nil {
			continue // Gone or renamed: it doesn't hold the others back
		}
		if replicas == 0 || ready == 0 {
			up = false
		}
		if i >= len(chain)-len(own) && replicas > 0 {
			awake = true
		}
	}

	seen, tracked := w.wakes[route.ID]
	switch {
	case woken.IsZero() || !awake:
		// Never woken by Smart Proxy (scaled up by hand: as usual), or asleep.
		delete(w.wakes, route.ID)
		return false
	case up:
		if tracked {
			delete(w.wakes, route.ID)
			w.store.UpdateActivity(route.ID)
			logger.Printf("Route %s%s ready about %s after its wake-up: its idle timeout starts now",
				route.Host, route.Path, now.Sub(seen).Round(time.Second))
		}
		return false
	case now.Sub(woken) < startupLimit:
		w.wakes[route.ID] = woken
		return true
	default:
		if tracked {
			delete(w.wakes, route.ID)
			logger.Printf("Route %s%s still not ready %s after its wake-up: it sleeps when idle, as usual",
				route.Host, route.Path, startupLimit)
		}
		return false
	}
}
