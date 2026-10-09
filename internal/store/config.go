// Package store handles the persistence and in-memory management of route configurations.
// Routes persist to a JSON file (single instance, offline) or a ConfigMap shared by every
// replica; request activity is tracked in memory.
package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"smart-proxy/internal/traffic"
)

// DependencyConfig defines a dependent deployment that should be managed alongside the main route.
type DependencyConfig struct {
	Name       string `json:"name"`
	StopOnIdle bool   `json:"stop_on_idle"`
}

// RouteConfig represents the configuration for a single proxied route.
type RouteConfig struct {
	ID   string `json:"id"`
	Host string `json:"host"` // Domain to match (e.g. app.local)
	Path string `json:"path"` // URL Path to match
	// PathExact matches the path only, not what is under it (Ingress pathType Exact).
	PathExact     bool               `json:"path_exact,omitempty"`
	TargetService string             `json:"target_service"`
	TargetPort    int                `json:"target_port"`
	Namespace     string             `json:"namespace"`
	Deployment    string             `json:"deployment"`
	Dependencies  []DependencyConfig `json:"dependencies"` // List of dependent deployments
	IdleTimeout   time.Duration      `json:"idle_timeout"`
	LastActivity  time.Time          `json:"last_activity"`
	InjectBadge   bool               `json:"inject_badge"` // If true, injects a visible badge in HTML responses
	AlwaysOn      bool               `json:"always_on"`    // If true, the main deployment is not scaled down on idle
	// StartInOrder wakes the dependencies one at a time, in the listed order, each once the
	// previous one is ready, and the main deployment last (e.g. database, then API, then app).
	// Otherwise everything is woken at once.
	StartInOrder bool `json:"start_in_order"`
	// Schedule keeps the route awake during given hours, regardless of traffic.
	Schedule *Schedule `json:"schedule,omitempty"`
	// Declarative routes are defined by annotations on their Ingress/Route (smart-proxy/enabled);
	// changes made elsewhere are overwritten by those annotations.
	Declarative bool `json:"declarative,omitempty"`
	// Ignore selects requests that don't count as activity (uptime monitors, health checks),
	// on top of the global rules. They never wake the route; see WhenAsleep.
	Ignore *traffic.Rules `json:"ignore,omitempty"`
	// WhenAsleep is the answer to ignored requests while the route sleeps: WhenAsleepRespond
	// (default), WhenAsleepUnavailable or WhenAsleepWake.
	WhenAsleep string `json:"when_asleep,omitempty"`
	// InspectUntil, while in the future, has the requests to this route recorded for the
	// dashboard (see package inspect).
	InspectUntil *time.Time `json:"inspect_until,omitempty"`
	// Backends are the Services traffic is balanced across, by weight (from an OpenShift Route's
	// alternate backends). Empty means the single TargetService. See WeightedBackend.Managed.
	Backends []WeightedBackend `json:"backends,omitempty"`
}

// WeightedBackend is one of the Services a route balances traffic across.
type WeightedBackend struct {
	Service  string `json:"service"`
	Port     int    `json:"port"`
	Weight   int32  `json:"weight"`
	Workload string `json:"workload,omitempty"` // The workload behind the Service, if known
	// Managed backends are woken and put to sleep with the route. Others are left alone: they
	// get their share of traffic only while they run (as with the OpenShift router), so a
	// backend kept off on purpose stays off.
	Managed bool `json:"managed"`
	// When sends requests matching any of these conditions here, whatever the weights; the
	// client then stays on this backend (cookie). A backend with weight 0 gets only those.
	When []Condition `json:"when,omitempty"`
}

// Fields a backend condition looks at.
const (
	FieldHeader = "header"
	FieldCookie = "cookie"
	FieldQuery  = "query"
	FieldPath   = "path"
	FieldClient = "client" // Client IP or CIDR
)

// Comparisons of a backend condition.
const (
	OpEquals   = "equals"
	OpContains = "contains"
	OpPrefix   = "prefix"
	OpExists   = "exists"
)

// Condition matches a request, e.g. header Origin equals https://login.example.com.
type Condition struct {
	Field string `json:"field"`
	Name  string `json:"name,omitempty"` // Header, cookie or query parameter name
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
}

// Validate reports an incomplete or invalid condition.
func (c Condition) Validate() error {
	switch c.Field {
	case FieldHeader, FieldCookie, FieldQuery:
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("a %s condition needs a name", c.Field)
		}
		switch c.Op {
		case OpExists:
			return nil
		case OpEquals, OpContains, OpPrefix:
		default:
			return fmt.Errorf("unknown comparison %q", c.Op)
		}
	case FieldPath:
		if c.Op != OpEquals && c.Op != OpPrefix {
			return fmt.Errorf("a path condition compares with equals or prefix")
		}
		if !strings.HasPrefix(c.Value, "/") {
			return fmt.Errorf("path %q must start with /", c.Value)
		}
	case FieldClient:
		if _, err := traffic.ParseTrustedProxies([]string{c.Value}); err != nil || strings.TrimSpace(c.Value) == "" {
			return fmt.Errorf("invalid client IP or CIDR %q", c.Value)
		}
		return nil
	default:
		return fmt.Errorf("unknown condition field %q", c.Field)
	}
	if c.Value == "" {
		return fmt.Errorf("the %s condition on %q needs a value", c.Field, c.Name)
	}
	return nil
}

// WakeWorkloads are the workloads a request to the route wakes: its main workload and those of
// its managed backends taking a share of traffic. A backend reached only through conditions
// (weight 0) is woken when a request is sent to it.
func (r RouteConfig) WakeWorkloads() []string {
	workloads := []string{r.Deployment}
	for _, b := range r.Backends {
		if b.Managed && b.Weight > 0 && b.Workload != "" && b.Workload != r.Deployment {
			workloads = append(workloads, b.Workload)
		}
	}
	return workloads
}

// ManagedWorkloads are the route's own workloads: its main workload and those of its managed
// backends (dependencies aside). They sleep with the route.
func (r RouteConfig) ManagedWorkloads() []string {
	workloads := []string{r.Deployment}
	for _, b := range r.Backends {
		if b.Managed && b.Workload != "" && b.Workload != r.Deployment {
			workloads = append(workloads, b.Workload)
		}
	}
	return workloads
}

// NormalizeBackends makes the main target the first managed backend. It fails when backends
// are set but none is managed (the route would have nothing to wake).
func (r *RouteConfig) NormalizeBackends() error {
	if len(r.Backends) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, b := range r.Backends {
		if b.Service == "" || b.Port <= 0 {
			return fmt.Errorf("every backend needs a Service and a port")
		}
		if seen[b.Service] {
			return fmt.Errorf("backend %s is listed twice", b.Service)
		}
		seen[b.Service] = true
		if b.Weight < 0 || b.Weight > 256 {
			return fmt.Errorf("backend %s: weight must be between 0 and 256", b.Service)
		}
		for _, c := range b.When {
			if err := c.Validate(); err != nil {
				return fmt.Errorf("backend %s: %w", b.Service, err)
			}
		}
	}
	// The main target takes a share of traffic if any managed backend does.
	for _, takesShare := range []bool{true, false} {
		for _, b := range r.Backends {
			if b.Managed && (b.Weight > 0 || !takesShare) {
				r.TargetService, r.TargetPort = b.Service, b.Port
				if b.Workload != "" {
					r.Deployment = b.Workload
				}
				return nil
			}
		}
	}
	return fmt.Errorf("at least one backend must be managed by Smart Proxy")
}

// Answers to ignored requests (e.g. uptime monitors) while a route sleeps.
const (
	WhenAsleepRespond     = "respond"     // 200 from Smart Proxy: the monitor stays green, the app sleeps
	WhenAsleepUnavailable = "unavailable" // 503: the monitor reports the app down
	WhenAsleepWake        = "wake"        // Wake the app (the request still doesn't count as activity)
)

// ValidateTraffic reports invalid ignore rules or WhenAsleep values.
func (r RouteConfig) ValidateTraffic() error {
	switch r.WhenAsleep {
	case "", WhenAsleepRespond, WhenAsleepUnavailable, WhenAsleepWake:
	default:
		return fmt.Errorf("when_asleep must be %s, %s or %s", WhenAsleepRespond, WhenAsleepUnavailable, WhenAsleepWake)
	}
	if r.Ignore != nil {
		return r.Ignore.Validate()
	}
	return nil
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
	r.InspectUntil = nil // Runtime state, not configuration
	data, _ := json.Marshal(r)
	return string(data)
}

// Inspecting reports whether the route's requests are being recorded.
func (r RouteConfig) Inspecting(now time.Time) bool {
	return r.InspectUntil != nil && now.Before(*r.InspectUntil)
}
