package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromFileTreatsRoutesAsActiveAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	stale := []*RouteConfig{{ID: "ing-app", LastActivity: time.Now().Add(-24 * time.Hour)}}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	s := NewStore(path)

	r, ok := s.GetRoute("ing-app")
	if !ok {
		t.Fatal("route not loaded")
	}
	if r.LastActivity.Before(s.startedAt) {
		t.Errorf("LastActivity = %v, want not before store start %v", r.LastActivity, s.startedAt)
	}
}

func TestAddRouteClampsStaleActivity(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "routes.json"))

	// Simulates a route re-synced from an annotation written long ago.
	cfg := &RouteConfig{ID: "route-app", LastActivity: time.Now().Add(-24 * time.Hour)}
	if err := s.AddRoute(cfg); err != nil {
		t.Fatal(err)
	}

	r, _ := s.GetRoute("route-app")
	if r.LastActivity.Before(s.startedAt) {
		t.Errorf("LastActivity = %v, want not before store start %v", r.LastActivity, s.startedAt)
	}
}

func TestAddRouteKeepsNewerRecordedActivity(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err := s.AddRoute(&RouteConfig{ID: "route-app"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	s.UpdateActivity("route-app")
	r, _ := s.GetRoute("route-app")
	recorded := r.LastActivity

	// Re-adding with an older timestamp must not rewind the activity clock.
	if err := s.AddRoute(&RouteConfig{ID: "route-app", LastActivity: s.startedAt}); err != nil {
		t.Fatal(err)
	}

	r, _ = s.GetRoute("route-app")
	if !r.LastActivity.Equal(recorded) {
		t.Errorf("LastActivity = %v, want %v", r.LastActivity, recorded)
	}
}

func TestBackendConditionsAreValidated(t *testing.T) {
	route := func(c Condition) *RouteConfig {
		return &RouteConfig{Backends: []WeightedBackend{
			{Service: "a", Port: 80, Weight: 100, Managed: true},
			{Service: "b", Port: 80, Weight: 0, When: []Condition{c}},
		}}
	}
	valid := []Condition{
		{Field: FieldHeader, Name: "Origin", Op: OpEquals, Value: "https://partner"},
		{Field: FieldCookie, Name: "beta", Op: OpExists},
		{Field: FieldPath, Op: OpPrefix, Value: "/api/v2"},
		{Field: FieldClient, Value: "10.0.0.0/8"},
	}
	for _, c := range valid {
		if err := route(c).NormalizeBackends(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	invalid := []Condition{
		{Field: FieldHeader, Op: OpEquals, Value: "x"},     // no name
		{Field: FieldHeader, Name: "Origin", Op: OpEquals}, // no value
		{Field: FieldPath, Op: OpContains, Value: "/x"},    // path compares with equals/prefix
		{Field: FieldPath, Op: OpPrefix, Value: "api/v2"},  // relative
		{Field: FieldClient, Value: "not-an-ip"},
		{Field: "body", Op: OpEquals, Value: "x"},
	}
	for _, c := range invalid {
		if err := route(c).NormalizeBackends(); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	r := route(valid[0])
	r.NormalizeBackends()
	if r.TargetService != "a" {
		t.Errorf("main target = %q, want the backend taking traffic", r.TargetService)
	}
}
