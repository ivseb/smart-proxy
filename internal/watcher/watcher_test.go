package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

const proxyService = "sp"

func dep(ns, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
}

func ing(ns, name, service string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: name + ".example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: service, Port: networkingv1.ServiceBackendPort{Number: 8080}},
				}}},
			}},
		}}},
	}
}

func replicas(t *testing.T, c *fakecluster.Cluster, ns, name string) int32 {
	t.Helper()
	d, err := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return *d.Spec.Replicas
}

func TestIdleRoutesSleepPerNamespace(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a", "team-b"}}, fakecluster.Options{},
		dep("team-a", "web", 1), dep("team-b", "web", 1), dep("team-b", "manual", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)

	idle := &store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", IdleTimeout: time.Minute}
	active := &store.RouteConfig{ID: store.IngressID("team-b", "web"), Namespace: "team-b", Deployment: "web", IdleTimeout: time.Minute}
	manual := &store.RouteConfig{ID: "3f2a", Namespace: "team-b", Deployment: "manual", IdleTimeout: time.Minute}
	for _, r := range []*store.RouteConfig{idle, active, manual} {
		st.AddRoute(r)
	}
	// Make two routes idle; the same deployment name in team-b stays active.
	for _, id := range []string{idle.ID, manual.ID} {
		st.SetActivityForTest(id, time.Now().Add(-time.Hour))
	}

	w.checkIdleRoutes()

	if got := replicas(t, c, "team-a", "web"); got != 0 {
		t.Errorf("idle team-a/web: replicas=%d, want 0", got)
	}
	if got := replicas(t, c, "team-b", "web"); got != 1 {
		t.Errorf("active team-b/web: replicas=%d, want 1", got)
	}
	if got := replicas(t, c, "team-b", "manual"); got != 1 {
		t.Errorf("manual route must never sleep: replicas=%d", got)
	}
}

func TestHealReappliesRevertedIngressPatch(t *testing.T) {
	ingress := ing("team-a", "web", "web-svc")
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, ingress)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", TargetService: "web-svc", TargetPort: 8080})

	// The Ingress is in its original state, as after a Helm upgrade of the application.
	w.healUnpatchedRoutes()

	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if !k8s.IsIngressPatched(got, proxyService) {
		t.Fatalf("not re-patched: %+v", got.Spec.Rules[0].HTTP.Paths[0].Backend)
	}
	if got.Annotations[k8s.AnnotationOriginalService] != "web-svc" || got.Annotations[k8s.AnnotationOriginalPort] != "8080" {
		t.Fatalf("original backend not recorded: %v", got.Annotations)
	}
}

func TestHealMigratesLegacyNumericPortPatch(t *testing.T) {
	// Patched by an older version: legacy Service name and a numeric port.
	legacy := ing("team-a", "web", k8s.LegacyServiceName)
	legacy.Annotations = map[string]string{k8s.AnnotationPatched: "true", k8s.AnnotationOriginalService: "web-svc", k8s.AnnotationOriginalPort: "8080"}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, legacy)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	// Legacy route ID without a namespace.
	st.AddRoute(&store.RouteConfig{ID: "ing-web", Namespace: "team-a", Deployment: "web", TargetService: "web-svc", TargetPort: 8080})

	NewWatcher(c.Client, st, proxyService).healUnpatchedRoutes()

	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if !k8s.IsIngressPatched(got, proxyService) || got.Annotations[k8s.AnnotationOriginalService] != "web-svc" {
		t.Fatalf("not migrated: backend=%+v annotations=%v", got.Spec.Rules[0].HTTP.Paths[0].Backend, got.Annotations)
	}
}
