package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

const ns = "team-a"

func sleeping(name string) *appsv1.Deployment {
	zero := int32(0)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: appsv1.DeploymentSpec{Replicas: &zero}}
}

func newHandler(t *testing.T, objs ...*appsv1.Deployment) (*Handler, *fakecluster.Cluster) {
	t.Helper()
	var runtimeObjs []runtime.Object
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, fakecluster.Options{}, runtimeObjs...)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	return &Handler{k8sClient: c.Client, store: st, Metrics: NewMetrics()}, c
}

func replicas(t *testing.T, c *fakecluster.Cluster, name string) int32 {
	t.Helper()
	d, err := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return *d.Spec.Replicas
}

// markReady simulates the deployment's pods becoming ready and waits for the cache to see it.
func markReady(t *testing.T, c *fakecluster.Cluster, name string) {
	t.Helper()
	d, _ := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	d.Status.ReadyReplicas = *d.Spec.Replicas
	if _, err := c.Kube.AppsV1().Deployments(ns).UpdateStatus(context.TODO(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fakecluster.Eventually(t, func() bool {
		_, ready, _ := c.GetDeploymentStatus(ns, name)
		return ready > 0
	}, name+" not ready in cache")
}

func waitScaled(t *testing.T, c *fakecluster.Cluster, name string) {
	t.Helper()
	fakecluster.Eventually(t, func() bool { r, _, _ := c.GetDeploymentStatus(ns, name); return r > 0 }, name+" not scaled in cache")
}

func route(inOrder bool) store.RouteConfig {
	return store.RouteConfig{
		ID: store.IngressID(ns, "web"), Namespace: ns, Deployment: "web", StartInOrder: inOrder,
		Dependencies: []store.DependencyConfig{{Name: "db"}, {Name: "api"}},
	}
}

func TestStartInOrderWakesOneAtATime(t *testing.T) {
	h, c := newHandler(t, sleeping("db"), sleeping("api"), sleeping("web"))
	routes := []store.RouteConfig{route(true)}

	step := func(wantAwake ...string) {
		t.Helper()
		_, ready := h.ensureAwake(routes)
		if ready {
			t.Fatal("reported ready too early")
		}
		awake := map[string]bool{}
		for _, n := range wantAwake {
			awake[n] = true
		}
		for _, name := range []string{"db", "api", "web"} {
			if got := replicas(t, c, name) > 0; got != awake[name] {
				t.Fatalf("%s awake = %v, want %v", name, got, awake[name])
			}
		}
	}

	step("db") // Only the first dependency starts
	waitScaled(t, c, "db")
	step("db") // Still waiting for db to be ready
	markReady(t, c, "db")
	step("db", "api")
	waitScaled(t, c, "api")
	markReady(t, c, "api")
	step("db", "api", "web") // The app itself comes last
	waitScaled(t, c, "web")
	markReady(t, c, "web")

	if states, ready := h.ensureAwake(routes); !ready || len(states) != 3 {
		t.Fatalf("final: ready=%v states=%+v", ready, states)
	}
}

func TestWithoutOrderEverythingWakesAtOnce(t *testing.T) {
	h, c := newHandler(t, sleeping("db"), sleeping("api"), sleeping("web"))
	if _, ready := h.ensureAwake([]store.RouteConfig{route(false)}); ready {
		t.Fatal("reported ready while asleep")
	}
	for _, name := range []string{"db", "api", "web"} {
		if replicas(t, c, name) == 0 {
			t.Errorf("%s not woken", name)
		}
	}
}

func TestStatusCheckSubpathAndHost(t *testing.T) {
	h, _ := newHandler(t, sleeping("web"))
	h.SetReady()
	_ = h.store.AddRoute(&store.RouteConfig{
		ID:            "route-test",
		Host:          "internal-route.example.com",
		Path:          "/subapp",
		TargetService: "web-svc",
		TargetPort:    8080,
		Namespace:     ns,
		Deployment:    "web",
	})

	// Request from external domain (client host parameter != route host, but HTTP Host header matches route host)
	// and endpoint is prefixed with the application subpath.
	req := httptest.NewRequest("GET", "/subapp/dashboard/__smart_proxy/status?path=%2Fsubapp%2Fdashboard&host=external-public.example.com", nil)
	req.Host = "internal-route.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	if res["status"] != "waiting" && res["status"] != "ready" {
		t.Fatalf("unexpected status in response: %v", res["status"])
	}
}
