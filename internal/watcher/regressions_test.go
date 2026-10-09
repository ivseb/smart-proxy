package watcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"smart-proxy/internal/declarative"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

func osRoute(ns, name, host, path, svc string) *routev1.Route {
	return &routev1.Route{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: routev1.RouteSpec{Host: host, Path: path, To: routev1.RouteTargetReference{Kind: "Service", Name: svc},
			Port: &routev1.RoutePort{TargetPort: intstr.FromInt(8080)}}}
}

// Self-healing must not patch a Route serving the same host on another path: another application.
func TestHealLeavesSiblingRoutesOfOtherPathsAlone(t *testing.T) {
	a := osRoute("team-a", "api", "app.example.com", "/api", "api-svc")
	b := osRoute("team-a", "front", "app.example.com", "/", "front-svc")
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, a, b)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	// Route "api" was patched via the dashboard.
	cfg := &store.RouteConfig{ID: store.RouteID("team-a", "api"), Namespace: "team-a", Host: "app.example.com", Path: "/api",
		Deployment: "api", TargetService: "api-svc", TargetPort: 8080}
	st.AddRoute(cfg)
	rt, _ := c.Routes.RouteV1().Routes("team-a").Get(context.TODO(), "api", metav1.GetOptions{})
	k8s.PatchRoute(rt, proxyService, k8s.Backend{Service: "api-svc", Port: 8080}, cfg.AnnotationJSON())
	c.Routes.RouteV1().Routes("team-a").Update(context.TODO(), rt, metav1.UpdateOptions{})
	fakecluster.Eventually(t, func() bool {
		r, err := c.GetRoute("team-a", "api")
		return err == nil && k8s.IsRoutePatched(r, proxyService)
	}, "cache")

	w.healUnpatchedRoutes()

	front, _ := c.Routes.RouteV1().Routes("team-a").Get(context.TODO(), "front", metav1.GetOptions{})
	if k8s.IsRoutePatched(front, proxyService) {
		t.Errorf("unrelated sibling Route 'front' (path /) was patched to the proxy; config=%s", front.Annotations[k8s.AnnotationConfig])
	}
}

// An idle route must not put to sleep a workload an Always On route shares.
func TestAlwaysOnRouteKeepsASharedWorkloadUp(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, dep("team-a", "web", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	pub := &store.RouteConfig{ID: store.IngressID("team-a", "public"), Namespace: "team-a", Deployment: "web", AlwaysOn: true}
	internal := &store.RouteConfig{ID: store.IngressID("team-a", "internal"), Namespace: "team-a", Deployment: "web"}
	st.AddRoute(pub)
	st.AddRoute(internal)
	st.SetActivityForTest(pub.ID, time.Now().Add(-2*time.Hour))
	st.SetActivityForTest(internal.ID, time.Now().Add(-2*time.Hour))
	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "web") == 0 {
		t.Error("Always On workload put to sleep because another idle route shares it")
	}
}

// "deployment/db" and "db" are the same workload: an active route still needs it.
func TestWorkloadAliasesAreTheSameWorkload(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, dep("team-a", "db", 1), dep("team-a", "a", 1), dep("team-a", "b", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	idle := &store.RouteConfig{ID: store.IngressID("team-a", "a"), Namespace: "team-a", Deployment: "a",
		Dependencies: []store.DependencyConfig{{Name: "deployment/db", StopOnIdle: true}}}
	active := &store.RouteConfig{ID: store.IngressID("team-a", "b"), Namespace: "team-a", Deployment: "b",
		Dependencies: []store.DependencyConfig{{Name: "db", StopOnIdle: true}}}
	st.AddRoute(idle)
	st.AddRoute(active)
	st.SetActivityForTest(idle.ID, time.Now().Add(-2*time.Hour))
	w.checkIdleRoutes()
	if replicas(t, c, "team-a", "db") == 0 {
		t.Error("db slept although active route b depends on it (alias mismatch)")
	}
}

// A Helm upgrade re-applied a declarative Ingress pointing at a new Service, keeping the old annotations.
func TestDeclarativeFollowsARedeployedService(t *testing.T) {
	ingress := ing("team-a", "web", "web-v2")
	ingress.Annotations = map[string]string{
		declarative.Enabled: "true", k8s.AnnotationDeclarative: "true",
		k8s.AnnotationPatched: "true", k8s.AnnotationOriginalService: "web-v1", k8s.AnnotationOriginalPort: "8080",
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web-v2", Namespace: "team-a"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}}}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, ingress, svc, dep("team-a", "web", 1))
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	w := NewWatcher(c.Client, st, proxyService)
	w.reconcileDeclarative()
	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	r, _ := st.GetRoute(store.IngressID("team-a", "web"))
	if got.Annotations[k8s.AnnotationOriginalService] != "web-v2" || r == nil || r.TargetService != "web-v2" {
		t.Errorf("original-service=%q store target=%v (live spec said web-v2); deployment=%q", got.Annotations[k8s.AnnotationOriginalService], r.TargetService, r.Deployment)
	}
}

// Self-healing an Ingress re-deployed with a new Service must forward to that Service.
func TestHealFollowsARedeployedService(t *testing.T) {
	ingress := ing("team-a", "web", "web-v2")
	ingress.Annotations = map[string]string{k8s.AnnotationPatched: "true", k8s.AnnotationOriginalService: "web-v1", k8s.AnnotationOriginalPort: "8080"}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, ingress)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	st.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", TargetService: "web-v1", TargetPort: 8080})
	NewWatcher(c.Client, st, proxyService).healUnpatchedRoutes()
	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	r, _ := st.GetRoute(store.IngressID("team-a", "web"))
	if got.Annotations[k8s.AnnotationOriginalService] != "web-v2" || r.TargetService != "web-v2" {
		t.Errorf("original-service=%q, proxy forwards to %s; want web-v2", got.Annotations[k8s.AnnotationOriginalService], r.TargetService)
	}
}

// A Route still pointing at Smart Proxy whose annotations were stripped: healing must not record
// Smart Proxy's own port as the original, or unpatching would break the Route.
func TestHealRebuildsARouteThatLostItsAnnotations(t *testing.T) {
	rt := osRoute("team-a", "api", "api.example.com", "/", proxyService)
	rt.Spec.Port = &routev1.RoutePort{TargetPort: intstr.FromString(k8s.ProxyPortName)}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api-svc", Namespace: "team-a"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080)}}}}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a"}}, fakecluster.Options{}, rt, svc)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	st.AddRoute(&store.RouteConfig{ID: store.RouteID("team-a", "api"), Namespace: "team-a", Host: "api.example.com",
		Deployment: "api", TargetService: "api-svc", TargetPort: 80})
	fakecluster.Eventually(t, func() bool { _, err := c.ResolveServicePort("team-a", "api-svc", nil); return err == nil }, "cache")

	NewWatcher(c.Client, st, proxyService).healUnpatchedRoutes()

	got, _ := c.Routes.RouteV1().Routes("team-a").Get(context.TODO(), "api", metav1.GetOptions{})
	if err := k8s.UnpatchRoute(got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.To.Name != "api-svc" || got.Spec.Port == nil || got.Spec.Port.TargetPort.StrVal != "http" {
		t.Fatalf("restored to %s port %+v", got.Spec.To.Name, got.Spec.Port)
	}
}
