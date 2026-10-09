package watcher

import (
	"context"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

// Removing a namespace from the scope must not leave its apps asleep behind patched Ingresses.
func TestNamespaceLeavingTheScopeIsRestored(t *testing.T) {
	selector, _ := labels.Parse("smart-proxy=enabled")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"smart-proxy": "enabled"}}}
	c := fakecluster.New(t, k8s.Scope{All: true, Selector: selector}, fakecluster.Options{}, ns, ing("team-a", "web", "web-svc"), dep("team-a", "web", 2))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	route := &store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Host: "web.example.com",
		Deployment: "web", TargetService: "web-svc", TargetPort: 8080}
	st.AddRoute(route)
	fakecluster.Eventually(t, func() bool { _, err := c.GetIngress("team-a", "web"); return err == nil }, "cache")
	w := NewWatcher(c.Client, st, proxyService)
	w.healUnpatchedRoutes() // Patches it
	if _, err := c.SleepDeployment("team-a", "web"); err != nil {
		t.Fatal(err)
	}

	ns.Labels = nil
	c.Kube.CoreV1().Namespaces().Update(context.TODO(), ns, metav1.UpdateOptions{})
	fakecluster.Eventually(t, func() bool { return !c.Watches("team-a") }, "still watched")
	w.releaseUnwatched()

	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if got.Annotations[k8s.AnnotationPatched] == "true" || got.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != "web-svc" {
		t.Fatalf("Ingress still patched: %+v", got.Spec.Rules[0].HTTP.Paths[0].Backend.Service)
	}
	if replicas(t, c, "team-a", "web") != 2 {
		t.Fatalf("workload not woken: %d replicas", replicas(t, c, "team-a", "web"))
	}
	if _, ok := st.GetRoute(route.ID); !ok {
		t.Fatal("route removed: it should be patched again if the namespace comes back")
	}
}
