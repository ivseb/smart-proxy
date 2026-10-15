package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"smart-proxy/internal/blackout"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

// blockedSince sets blocked hours (UTC, every day) that started ago and last another hour.
func blockedSince(t *testing.T, ago time.Duration) *blackout.Keeper {
	t.Helper()
	now := time.Now().UTC()
	s := blackout.Defaults()
	s.Timezone = "UTC"
	s.Global = store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{
		{From: now.Add(-ago).Format("15:04"), To: now.Add(time.Hour).Format("15:04")},
	}}
	k := &blackout.Keeper{}
	if err := k.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestBlockedHoursPutAppsToSleepWhenTheyStart(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{},
		dep("team-a", "web", 1), dep("team-a", "db", 1), dep("team-a", "always", 1), dep("team-a", "sched", 0), dep("team-a", "kept", 0))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	w.Blackout = blockedSince(t, time.Minute)

	now := time.Now().UTC()
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", IdleTimeout: time.Hour,
		Dependencies: []store.DependencyConfig{{Name: "db", StopOnIdle: true}}})
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "always"), Namespace: "team-a", Deployment: "always", AlwaysOn: true})
	// A schedule keeping it awake doesn't wake it during blocked hours.
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "sched"), Namespace: "team-a", Deployment: "sched",
		Schedule: &store.Schedule{From: now.Add(-2 * time.Hour).Format("15:04"), To: now.Add(2 * time.Hour).Format("15:04")}})
	// An exception keeping it on wakes it, blocked hours or not.
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "kept"), Namespace: "team-a", Deployment: "kept",
		Exceptions: []store.Exception{{ID: "x", Kind: store.ExceptionOn, From: now.Add(-time.Minute), To: now.Add(time.Hour)}}})
	st.UpdateActivity(store.IngressID("team-a", "web")) // Busy: put to sleep all the same

	w.checkIdleRoutes()

	for name, want := range map[string]int32{"web": 0, "db": 0, "always": 1, "sched": 0, "kept": 1} {
		if got := replicas(t, c, "team-a", name); got != want {
			t.Errorf("%s: replicas = %d, want %d", name, got, want)
		}
	}
}

// Woken by hand during blocked hours, an application isn't put to sleep again right away: only
// when blocked hours start.
func TestBlockedHoursOnlyActWhenTheyStart(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, dep("team-a", "web", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	w.Blackout = blockedSince(t, time.Hour)
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", IdleTimeout: time.Hour})
	st.UpdateActivity(store.IngressID("team-a", "web"))

	w.checkIdleRoutes()

	if got := replicas(t, c, "team-a", "web"); got != 1 {
		t.Fatalf("woken by hand during blocked hours: replicas = %d", got)
	}
}
