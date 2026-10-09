package watcher

import (
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
)

// releaseRetry is how often restoring an unwatched namespace is tried again after a failure.
const releaseRetry = 10 * time.Minute

// releaseUnwatched restores the routes of namespaces Smart Proxy no longer watches (removed from
// the list, or no longer matching the selector): their Ingresses/Routes point at the
// applications again and their workloads are woken, since nothing would wake them anymore.
// The routes themselves are kept: if the namespace is watched again, they are patched again.
func (w *Watcher) releaseUnwatched() {
	if w.k8sClient == nil {
		return
	}
	for _, route := range w.store.GetAllRoutes() {
		kind, ns, name, ok := route.Resource()
		if !ok {
			continue
		}
		if w.k8sClient.Watches(ns) {
			delete(w.released, route.ID)
			continue
		}
		if at, done := w.released[route.ID]; done && (at.IsZero() || time.Since(at) < releaseRetry) {
			continue
		}
		owns := func(o k8s.RouteOwner) bool {
			return o.Patched && (o.ID == route.ID || (o.ID == "" && o.Kind == kind && o.Name == name))
		}
		workloads := route.ManagedWorkloads()
		for _, d := range route.Dependencies {
			workloads = append(workloads, d.Name)
		}
		n, err := w.k8sClient.ReleaseNamespace(ns, owns, workloads)
		if err != nil {
			w.released[route.ID] = time.Now()
			logger.Printf("Namespace %s is no longer watched, but restoring route %s failed: %v. Its patched Ingresses/Routes and sleeping workloads need Smart Proxy's permissions there; restore them by hand, or watch the namespace again and delete the route.", ns, route.ID, err)
			continue
		}
		w.released[route.ID] = time.Time{} // Done for good (until watched again)
		if n > 0 {
			logger.Printf("Namespace %s is no longer watched: restored %d resource(s)/workload(s) of route %s (patched again if the namespace comes back)", ns, n, route.ID)
		}
	}
}
