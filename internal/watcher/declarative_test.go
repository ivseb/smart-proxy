package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/declarative"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

func TestDeclarativeLifecycle(t *testing.T) {
	ingress := ing("team-a", "web", "web-svc")
	ingress.Annotations = map[string]string{
		declarative.Enabled:      "true",
		declarative.IdleTimeout:  "45m",
		declarative.Dependencies: "statefulset/db:keep",
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "team-a"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, ingress, svc, dep("team-a", "web", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)

	get := func() *networkingv1.Ingress {
		got, err := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	waitCached := func(cond func(*networkingv1.Ingress) bool) {
		fakecluster.Eventually(t, func() bool { i, err := c.GetIngress("team-a", "web"); return err == nil && cond(i) }, "cache not updated")
	}

	// Opt in: patched, with a route following the annotations.
	w.reconcileDeclarative()
	if got := get(); !k8s.IsIngressPatched(got, proxyService) || got.Annotations[k8s.AnnotationDeclarative] != "true" {
		t.Fatalf("not patched: %+v", got.Annotations)
	}
	r, ok := st.GetRoute(store.IngressID("team-a", "web"))
	if !ok || !r.Declarative || r.IdleTimeout != 45*time.Minute || r.Deployment != "web" || r.TargetPort != 8080 ||
		len(r.Dependencies) != 1 || r.Dependencies[0].Name != "statefulset/db" || r.Dependencies[0].StopOnIdle {
		t.Fatalf("route = %+v", r)
	}

	// Changing an annotation updates the route.
	waitCached(func(i *networkingv1.Ingress) bool { return k8s.IsIngressPatched(i, proxyService) })
	updated := get()
	updated.Annotations[declarative.IdleTimeout] = "2h"
	c.Kube.NetworkingV1().Ingresses("team-a").Update(context.TODO(), updated, metav1.UpdateOptions{})
	waitCached(func(i *networkingv1.Ingress) bool { return i.Annotations[declarative.IdleTimeout] == "2h" })
	w.reconcileDeclarative()
	if r, _ := st.GetRoute(store.IngressID("team-a", "web")); r.IdleTimeout != 2*time.Hour {
		t.Fatalf("idle timeout not updated: %v", r.IdleTimeout)
	}

	// An invalid annotation is ignored (the last valid configuration stays).
	invalid := get()
	invalid.Annotations[declarative.IdleTimeout] = "whenever"
	c.Kube.NetworkingV1().Ingresses("team-a").Update(context.TODO(), invalid, metav1.UpdateOptions{})
	waitCached(func(i *networkingv1.Ingress) bool { return i.Annotations[declarative.IdleTimeout] == "whenever" })
	w.reconcileDeclarative()
	if r, _ := st.GetRoute(store.IngressID("team-a", "web")); r.IdleTimeout != 2*time.Hour {
		t.Fatalf("invalid annotation applied: %v", r.IdleTimeout)
	}

	// Opting out restores the Ingress and removes the route.
	optOut := get()
	optOut.Annotations[declarative.Enabled] = "false"
	c.Kube.NetworkingV1().Ingresses("team-a").Update(context.TODO(), optOut, metav1.UpdateOptions{})
	waitCached(func(i *networkingv1.Ingress) bool { return i.Annotations[declarative.Enabled] == "false" })
	w.reconcileDeclarative()
	got := get()
	if b := got.Spec.Rules[0].HTTP.Paths[0].Backend.Service; b.Name != "web-svc" || got.Annotations[k8s.AnnotationPatched] != "" {
		t.Fatalf("not restored: %+v %v", b, got.Annotations)
	}
	if _, ok := st.GetRoute(store.IngressID("team-a", "web")); ok {
		t.Fatal("route not removed")
	}
}

func TestDashboardPatchesAreNotUndoneByDeclarative(t *testing.T) {
	ingress := ing("team-a", "web", "web-svc")
	k8s.PatchIngress(ingress, proxyService, k8s.Backend{Service: "web-svc", Port: 8080}, "{}") // Patched from the dashboard
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, ingress)
	w := NewWatcher(c.Client, store.NewStore(filepath.Join(t.TempDir(), "routes.json")), proxyService)

	w.reconcileDeclarative()
	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if !k8s.IsIngressPatched(got, proxyService) {
		t.Fatal("dashboard patch was undone")
	}
}
