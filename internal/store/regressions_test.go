package store

import (
	"testing"
	"time"
)

type memBackend struct{ data []byte }

func (m *memBackend) Load() ([]*RouteConfig, error) { return DecodeRoutes(m.data) }
func (m *memBackend) Update(mutate func(map[string]*RouteConfig)) ([]*RouteConfig, error) {
	routes, _ := DecodeRoutes(m.data)
	mm := toMap(routes)
	mutate(mm)
	m.data, _ = EncodeRoutes(mm, false) // like the ConfigMap backend
	r, _ := DecodeRoutes(m.data)
	return r, nil
}

// A route created through another replica (which saves no activity) must not look idle on the
// leader since the leader started.
func TestRouteCreatedOnAnotherReplicaStartsActive(t *testing.T) {
	be := &memBackend{}
	leader := NewStoreWithBackend(be)
	leader.startedAt = time.Now().Add(-3 * time.Hour) // leader has been running for 3h
	follower := NewStoreWithBackend(be)
	follower.AddRoute(&RouteConfig{ID: "ing-a/web", Namespace: "a", Deployment: "web", IdleTimeout: 30 * time.Minute, LastActivity: time.Now()})
	// The leader's informer sees the ConfigMap change.
	routes, _ := be.Load()
	leader.Replace(routes)
	r, _ := leader.GetRoute("ing-a/web")
	if time.Since(r.LastActivity) > r.EffectiveIdleTimeout() {
		t.Errorf("freshly patched route is idle on the leader (LastActivity %s ago)", time.Since(r.LastActivity).Round(time.Minute))
	}
}
