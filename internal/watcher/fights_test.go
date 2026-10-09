package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

// A GitOps tool scaling the workload back up every time: after a few rounds, stop fighting.
func TestStopsSleepingAWorkloadSomethingKeepsScalingUp(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, dep("team-a", "web", 2))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	route := &store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", IdleTimeout: time.Minute}
	st.AddRoute(route)
	st.SetActivityForTest(route.ID, time.Now().Add(-time.Hour))

	gitOpsRevert := func() {
		d, _ := c.Kube.AppsV1().Deployments("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
		two := int32(2)
		d.Spec.Replicas = &two
		c.Kube.AppsV1().Deployments("team-a").Update(context.TODO(), d, metav1.UpdateOptions{})
		fakecluster.Eventually(t, func() bool { r, _, _ := c.GetDeploymentStatus("team-a", "web"); return r == 2 }, "cache")
	}
	sleeps := 0
	for round := 0; round < 6; round++ {
		w.checkIdleRoutes()
		if replicas(t, c, "team-a", "web") == 0 {
			sleeps++
			fakecluster.Eventually(t, func() bool { r, _, _ := c.GetDeploymentStatus("team-a", "web"); return r == 0 }, "cache")
			w.checkIdleRoutes() // Ticks while asleep don't count as a fight
			gitOpsRevert()
		}
	}
	if sleeps != fightRounds {
		t.Fatalf("slept %d times, want %d before stepping back", sleeps, fightRounds)
	}
}

func TestStopsHealingAResourceSomethingKeepsReverting(t *testing.T) {
	w := NewWatcher(nil, nil, proxyService)
	for i := 0; i < fightRounds; i++ {
		if !w.mayHeal("Ingress a/web") {
			t.Fatalf("refused heal %d", i+1)
		}
	}
	if w.mayHeal("Ingress a/web") || !w.mayHeal("Ingress a/other") {
		t.Fatal("kept healing a resource reverted every time, or stopped healing another one")
	}
}
