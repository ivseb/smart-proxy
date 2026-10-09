package store

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Backend persists route configurations.
type Backend interface {
	// Load returns the persisted routes.
	Load() ([]*RouteConfig, error)
	// Update atomically applies mutate to the persisted routes (keyed by ID) and returns the
	// result. Shared backends retry on concurrent changes, so mutate may run more than once.
	Update(mutate func(routes map[string]*RouteConfig)) ([]*RouteConfig, error)
}

// Store provides a thread-safe implementation for managing RouteConfigs.
type Store struct {
	writeMu sync.Mutex // Serializes writes to the backend

	mu      sync.RWMutex
	routes  map[string]*RouteConfig // Key is ID
	backend Backend
	// local is the activity seen by this instance, to be shared with other replicas.
	local     map[string]time.Time
	startedAt time.Time
	version   atomic.Uint64 // Changes whenever the configurations do (not their activity)
}

// Version changes whenever route configurations do, so readers can cache what they derive
// from them.
func (s *Store) Version() uint64 { return s.version.Load() }

// NewStore keeps routes in a JSON file.
func NewStore(filePath string) *Store {
	return NewStoreWithBackend(NewFileBackend(filePath))
}

// NewStoreWithBackend keeps routes in the given backend, loading what it already holds.
func NewStoreWithBackend(backend Backend) *Store {
	s := &Store{
		routes:    make(map[string]*RouteConfig),
		backend:   backend,
		local:     make(map[string]time.Time),
		startedAt: time.Now(),
	}
	if routes, err := backend.Load(); err == nil {
		s.Replace(routes)
	}
	if seeded, ok := backend.(interface{ SetSeed(func() []*RouteConfig) }); ok {
		seeded.SetSeed(func() []*RouteConfig {
			all := s.GetAllRoutes()
			list := make([]*RouteConfig, len(all))
			for i := range all {
				list[i] = &all[i]
			}
			return list
		})
	}
	return s
}

// Adopt replaces the routes with a version written elsewhere (e.g. by another replica), unless
// the backend knows that version predates a write made here.
func (s *Store) Adopt(version string, routes []*RouteConfig) {
	s.writeMu.Lock() // Not between a write here and the adoption of its result
	defer s.writeMu.Unlock()
	if v, ok := s.backend.(interface{ Stale(string) bool }); ok && v.Stale(version) {
		return
	}
	s.Replace(routes)
}

// AddRoute adds or updates a route. ID is generated if empty.
func (s *Store) AddRoute(config *RouteConfig) error {
	if config.ID == "" {
		config.ID = uuid.New().String()
	}
	stored := *config
	return s.update(func(routes map[string]*RouteConfig) {
		c := stored
		routes[c.ID] = &c
	}, config)
}

// RemoveRoute deletes a route.
func (s *Store) RemoveRoute(id string) error {
	return s.update(func(routes map[string]*RouteConfig) {
		delete(routes, id)
	}, nil)
}

// RemoveResource deletes every route bound to the given Ingress or Route.
func (s *Store) RemoveResource(kind, namespace, name string) error {
	return s.update(func(routes map[string]*RouteConfig) {
		for id, r := range routes {
			if k, ns, n, ok := r.Resource(); ok && k == kind && ns == namespace && n == name {
				delete(routes, id)
			}
		}
	}, nil)
}

// update writes through the backend and adopts its result. added, when set, is the route
// being saved: it takes the resulting activity so callers see the same state as the store.
func (s *Store) update(mutate func(map[string]*RouteConfig), added *RouteConfig) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	routes, err := s.backend.Update(mutate)
	if err != nil {
		return err
	}
	if added != nil {
		// A route saved here was just touched: don't let stale persisted activity rewind it.
		for _, r := range routes {
			if r.ID == added.ID && r.LastActivity.Before(added.LastActivity) {
				r.LastActivity = added.LastActivity
			}
		}
	}
	s.Replace(routes)
	if added != nil {
		if r, ok := s.GetRoute(added.ID); ok {
			added.LastActivity = r.LastActivity
		}
	}
	return nil
}

// Replace adopts a new set of routes (e.g. changed by another replica), keeping the activity
// already known for routes that still exist.
func (s *Store) Replace(routes []*RouteConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]*RouteConfig, len(routes))
	for _, r := range routes {
		c := *r
		if c.ID == "" {
			c.ID = uuid.New().String() // Assign ID to legacy routes
		}
		if existing, ok := s.routes[c.ID]; ok {
			if existing.LastActivity.After(c.LastActivity) {
				c.LastActivity = existing.LastActivity
			}
		} else if now := time.Now(); c.LastActivity.Before(now) {
			// New here, e.g. just created through another replica, which saves no activity:
			// its idle timeout starts now, not when this replica started.
			c.LastActivity = now
		}
		s.clampActivity(&c)
		next[c.ID] = &c
	}
	s.routes = next
	s.version.Add(1)
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

// UpdateActivity records a request for the route.
func (s *Store) UpdateActivity(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if route, exists := s.routes[id]; exists {
		now := time.Now()
		route.LastActivity = now
		s.local[id] = now
	}
}

// LocalActivity returns the last request time this instance saw for each route.
func (s *Store) LocalActivity() map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]time.Time, len(s.local))
	for id, t := range s.local {
		if _, exists := s.routes[id]; exists {
			out[id] = t
		}
	}
	return out
}

// MergeActivity adopts activity seen by other replicas, keeping the most recent time.
func (s *Store) MergeActivity(activity map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range activity {
		if r, ok := s.routes[id]; ok && t.After(r.LastActivity) {
			r.LastActivity = t
		}
	}
}

// GetAllRoutes returns copies of every route, sorted by host, path and ID.
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

// clampActivity ensures a route never appears idle since before this process started.
// Persisted or annotated timestamps are stale after a restart; without this, every route
// would be scaled down on the first watcher tick.
func (s *Store) clampActivity(r *RouteConfig) {
	if r.LastActivity.Before(s.startedAt) {
		r.LastActivity = s.startedAt
	}
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
