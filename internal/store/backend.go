package store

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"
)

// EncodeRoutes serializes routes for a backend, sorted by ID for stable output.
// LastActivity is runtime state and is left out unless keepActivity is set.
func EncodeRoutes(routes map[string]*RouteConfig, keepActivity bool) ([]byte, error) {
	list := make([]RouteConfig, 0, len(routes))
	for _, r := range routes {
		c := *r
		if !keepActivity {
			c.LastActivity = time.Time{}
		}
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if keepActivity {
		return json.MarshalIndent(list, "", "  ") // The file is meant to be readable
	}
	return json.Marshal(list) // Compact: the ConfigMap is limited to 1 MiB
}

// DecodeRoutes parses what EncodeRoutes produced (an empty input means no routes).
func DecodeRoutes(data []byte) ([]*RouteConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var routes []*RouteConfig
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func toMap(routes []*RouteConfig) map[string]*RouteConfig {
	m := make(map[string]*RouteConfig, len(routes))
	for _, r := range routes {
		m[r.ID] = r
	}
	return m
}

func toList(m map[string]*RouteConfig) []*RouteConfig {
	list := make([]*RouteConfig, 0, len(m))
	for _, r := range m {
		list = append(list, r)
	}
	return list
}

// FileBackend keeps routes in a local JSON file (one Smart Proxy instance, or offline).
type FileBackend struct {
	mu   sync.Mutex
	path string
}

func NewFileBackend(path string) *FileBackend {
	return &FileBackend{path: path}
}

func (b *FileBackend) Load() ([]*RouteConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.load()
}

func (b *FileBackend) load() ([]*RouteConfig, error) {
	data, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return DecodeRoutes(data)
}

func (b *FileBackend) Update(mutate func(map[string]*RouteConfig)) ([]*RouteConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	routes, err := b.load()
	if err != nil {
		return nil, err
	}
	m := toMap(routes)
	mutate(m)
	data, err := EncodeRoutes(m, true)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(b.path, data, 0644); err != nil {
		return nil, err
	}
	return toList(m), nil
}
