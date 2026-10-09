// Package store handles the persistence and in-memory management of route configurations.
// It supports saving routes to a JSON file and providing thread-safe access.
package store

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

// Store provides a thread-safe implementation for managing RouteConfigs.
type Store struct {
	mu        sync.RWMutex
	routes    map[string]*RouteConfig // Key is ID
	filePath  string
	startedAt time.Time
}

func NewStore(filePath string) *Store {
	s := &Store{
		routes:    make(map[string]*RouteConfig),
		filePath:  filePath,
		startedAt: time.Now(),
	}
	s.LoadFromFile()
	return s
}

// AddRoute adds or updates a route. ID is generated if empty.
func (s *Store) AddRoute(config *RouteConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if config.ID == "" {
		config.ID = uuid.New().String()
	}

	// Validate uniqueness? For now, we allow overrides or duplicates on different IDs.
	// In V2, we might want to check if Host+Path combo exists, but let's keep it simple.

	// Don't let a re-added route (e.g. synced from a stale annotation) lose recorded activity.
	if existing, ok := s.routes[config.ID]; ok && existing.LastActivity.After(config.LastActivity) {
		config.LastActivity = existing.LastActivity
	}
	s.clampActivity(config)

	// Store a copy: callers keep using their struct, which must not alias state that
	// UpdateActivity changes under the lock.
	stored := *config
	s.routes[config.ID] = &stored
	return s.saveToFile()
}

func (s *Store) RemoveRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, id)
	return s.saveToFile()
}

// GetRoute returns a copy of a route.
func (s *Store) GetRoute(id string) (*RouteConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config, exists := s.routes[id]
	if !exists {
		return nil, false
	}
	c := *config
	return &c, true
}

func (s *Store) UpdateActivity(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if route, exists := s.routes[id]; exists {
		route.LastActivity = time.Now()
	}
}

func (s *Store) GetAllRoutes() []RouteConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	routes := make([]RouteConfig, 0, len(s.routes))
	for _, r := range s.routes {
		routes = append(routes, *r)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Host != routes[j].Host {
			return routes[i].Host < routes[j].Host
		}
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].ID < routes[j].ID
	})
	return routes
}

func (s *Store) LoadFromFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var routes []*RouteConfig
	if err := json.Unmarshal(data, &routes); err != nil {
		return err
	}

	s.routes = make(map[string]*RouteConfig)
	for _, r := range routes {
		if r.ID == "" {
			r.ID = uuid.New().String() // Assign ID to legacy routes
		}
		s.clampActivity(r)
		s.routes[r.ID] = r
	}
	return nil
}

// clampActivity ensures a route never appears idle since before this process started.
// Activity is only tracked in memory, so persisted or annotated timestamps are stale after a
// restart; without this, every route would be scaled down on the first watcher tick.
func (s *Store) clampActivity(r *RouteConfig) {
	if r.LastActivity.Before(s.startedAt) {
		r.LastActivity = s.startedAt
	}
}

func (s *Store) saveToFile() error {
	routes := make([]*RouteConfig, 0, len(s.routes))
	for _, r := range s.routes {
		routes = append(routes, r)
	}

	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0644)
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

// RemoveResource deletes every route bound to the given Ingress or Route.
func (s *Store) RemoveResource(kind, namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.routes {
		if k, ns, n, ok := r.Resource(); ok && k == kind && ns == namespace && n == name {
			delete(s.routes, id)
		}
	}
	return s.saveToFile()
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

// SetActivityForTest overwrites a route's LastActivity, including rewinding it.
// Only for tests that need idle routes.
func (s *Store) SetActivityForTest(id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.routes[id]; ok {
		r.LastActivity = at
	}
}
