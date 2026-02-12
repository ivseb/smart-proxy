// Package store handles the persistence and in-memory management of route configurations.
// It supports saving routes to a JSON file and providing thread-safe access.
package store

import (
	"encoding/json"
	"os"
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
}

// Store provides a thread-safe implementation for managing RouteConfigs.
type Store struct {
	mu       sync.RWMutex
	routes   map[string]*RouteConfig // Key is ID
	filePath string
}

func NewStore(filePath string) *Store {
	s := &Store{
		routes:   make(map[string]*RouteConfig),
		filePath: filePath,
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

	s.routes[config.ID] = config
	return s.saveToFile()
}

func (s *Store) RemoveRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, id)
	return s.saveToFile()
}

func (s *Store) GetRoute(id string) (*RouteConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config, exists := s.routes[id]
	return config, exists
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
		s.routes[r.ID] = r
	}
	return nil
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
