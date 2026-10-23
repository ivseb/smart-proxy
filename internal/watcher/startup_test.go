package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

// woken is a workload Smart Proxy woke at the given time, with no ready replica yet.
func woken(ns, name string, at time.Time) runtime.Object {
	d := dep(ns, name, 1)
	d.Annotations = map[string]string{k8s.AnnotationWokenAt: at.UTC().Format(time.RFC3339)}
	return d
}

// A woken stack that takes longer than its idle timeout to start isn't put to sleep meanwhile,
// and its idle timeout starts once it serves.
func TestIdleTimeoutStartsWhenTheStackIsReady(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{},
		woken("team-a", "db", time.Now().Add(-5*time.Minute)), woken("team-a", "web", time.Now().Add(-5*time.Minute)))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	route := &store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web",
		IdleTimeout: time.Minute, Dependencies: []store.DependencyConfig{{Name: "db", StopOnIdle: true}}}
	st.AddRoute(route)
	st.SetActivityForTest(route.ID, time.Now().Add(-5*time.Minute)) // The request that woke it

	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "web") != 1 || replicas(t, c, "team-a", "db") != 1 {
		t.Fatal("put to sleep while starting")
	}

	// The app serves, the database doesn't yet: still starting.
	setReady(t, c, "team-a", "web")
	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "web") != 1 {
		t.Fatal("put to sleep before the whole stack was ready")
	}

	// All ready: the idle timeout starts now.
	setReady(t, c, "team-a", "db")
	w.checkIdleRoutes()
	if r, _ := st.GetRoute(route.ID); replicas(t, c, "team-a", "web") != 1 || time.Since(r.LastActivity) > time.Minute {
		t.Fatalf("ready: replicas %d, idle since %s", replicas(t, c, "team-a", "web"), r.LastActivity)
	}

	// Its idle timeout over: it sleeps.
	st.SetActivityForTest(route.ID, time.Now().Add(-2*time.Minute))
	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "web") != 0 || replicas(t, c, "team-a", "db") != 0 {
		t.Fatal("not put to sleep once idle")
	}
}

// A stack that never gets ready (crash loop) sleeps when idle once the startup limit is over.
func TestStackNeverReadySleepsAfterTheStartupLimit(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{},
		woken("team-a", "web", time.Now().Add(-startupLimit-time.Minute)))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	route := &store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", IdleTimeout: time.Minute}
	st.AddRoute(route)
	st.SetActivityForTest(route.ID, time.Now().Add(-time.Hour))

	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "web") != 0 {
		t.Fatal("a stack that never got ready kept running")
	}
}

func setReady(t *testing.T, c *fakecluster.Cluster, ns, name string) {
	t.Helper()
	d, _ := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	d.Status.Replicas, d.Status.ReadyReplicas = *d.Spec.Replicas, *d.Spec.Replicas
	if _, err := c.Kube.AppsV1().Deployments(ns).UpdateStatus(context.TODO(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fakecluster.Eventually(t, func() bool { _, ready, _ := c.GetDeploymentStatus(ns, name); return ready > 0 }, name+" not ready in cache")
}
