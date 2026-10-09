// Package store handles the persistence and in-memory management of route configurations.
// Routes persist to a JSON file (single instance, offline) or a ConfigMap shared by every
// replica; request activity is tracked in memory.
package store

import (
	"encoding/json"
	"strings"
	"time"
)

// DependencyConfig defines a dependent deployment that should be managed alongside the main route.
type DependencyConfig struct {
	Name       string `json:"name"`
	StopOnIdle bool   `json:"stop_on_idle"`
}

// RouteConfig represents the configuration for a single proxied route.
type RouteConfig struct {
	ID            string             `json:"id"`
	Host          string             `json:"host"` // Domain to match (e.g. app.local)
	Path          string             `json:"path"` // URL Path to match
	TargetService string             `json:"target_service"`
	TargetPort    int                `json:"target_port"`
	Namespace     string             `json:"namespace"`
	Deployment    string             `json:"deployment"`
	Dependencies  []DependencyConfig `json:"dependencies"` // List of dependent deployments
	IdleTimeout   time.Duration      `json:"idle_timeout"`
	LastActivity  time.Time          `json:"last_activity"`
	InjectBadge   bool               `json:"inject_badge"` // If true, injects a visible badge in HTML responses
	AlwaysOn      bool               `json:"always_on"`    // If true, the main deployment is not scaled down on idle
}

// Kinds of cluster resources a route can be bound to by patching.
const (
	KindIngress = "Ingress"
	KindRoute   = "Route"
)

// IngressID is the route ID for a patched Ingress.
func IngressID(namespace, name string) string { return "ing-" + namespace + "/" + name }

// RouteID is the route ID for a patched OpenShift Route.
func RouteID(namespace, name string) string { return "route-" + namespace + "/" + name }

// Resource returns the Ingress or Route a route was created by patching. IDs are
// "ing-<namespace>/<name>" or "route-<namespace>/<name>"; older versions stored
// "ing-<name>", which refers to the route's own namespace. Manual routes (UUIDs) have none.
func (r RouteConfig) Resource() (kind, namespace, name string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(r.ID, "ing-"):
		kind, rest = KindIngress, r.ID[len("ing-"):]
	case strings.HasPrefix(r.ID, "route-"):
		kind, rest = KindRoute, r.ID[len("route-"):]
	default:
		return "", "", "", false
	}
	if ns, n, found := strings.Cut(rest, "/"); found {
		return kind, ns, n, true
	}
	return kind, r.Namespace, rest, true
}

// DefaultIdleTimeout applies to routes saved without one; a zero timeout would put the
// deployment back to sleep on every watcher tick.
const DefaultIdleTimeout = 30 * time.Minute

// EffectiveIdleTimeout is the route's idle timeout, or DefaultIdleTimeout when unset.
func (r RouteConfig) EffectiveIdleTimeout() time.Duration {
	if r.IdleTimeout <= 0 {
		return DefaultIdleTimeout
	}
	return r.IdleTimeout
}

// AnnotationJSON is the configuration stored in the smart-proxy/config annotation of a patched
// Ingress/Route. LastActivity is runtime state; leaving it out avoids rewriting the annotation
// on every save.
func (r RouteConfig) AnnotationJSON() string {
	r.LastActivity = time.Time{}
	data, _ := json.Marshal(r)
	return string(data)
}
