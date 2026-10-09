package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
)

// recorder is a fake transport answering 200 and recording which Service each request went to.
type recorder struct {
	mu    sync.Mutex
	hosts []string
}

func (t *recorder) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.hosts = append(t.hosts, strings.SplitN(r.URL.Host, ".", 2)[0])
	t.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}, Request: r}, nil
}

func (t *recorder) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.hosts) == 0 {
		return ""
	}
	return t.hosts[len(t.hosts)-1]
}

func ready(name string, replicas int32) *appsv1.Deployment {
	d := sleeping(name)
	d.Spec.Replicas = &replicas
	d.Status.ReadyReplicas = replicas
	return d
}

// Route R balances 50/50 between S1 (managed) and S2, kept off on purpose.
func balancedHandler(t *testing.T, s1, s2 *appsv1.Deployment) (*Handler, *fakecluster.Cluster, *recorder) {
	t.Helper()
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, fakecluster.Options{}, []runtime.Object{s1, s2}...)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	route := &store.RouteConfig{ID: store.RouteID(ns, "r"), Host: "r.example.com", Path: "/", Namespace: ns,
		Backends: []store.WeightedBackend{
			{Service: "s1", Port: 8080, Weight: 50, Workload: "s1", Managed: true},
			{Service: "s2", Port: 8080, Weight: 50, Workload: "s2"},
		}}
	if err := route.NormalizeBackends(); err != nil {
		t.Fatal(err)
	}
	st.AddRoute(route)
	tr := &recorder{}
	h := &Handler{k8sClient: c.Client, store: st, Metrics: NewMetrics(), Transport: tr}
	h.SetReady()
	return h, c, tr
}

func visit(h *Handler, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://r.example.com/", nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11) Firefox/131.0")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func setReady(t *testing.T, c *fakecluster.Cluster, name string, replicas int32) {
	t.Helper()
	d, _ := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	d.Spec.Replicas = &replicas
	d.Status.ReadyReplicas = replicas
	c.Kube.AppsV1().Deployments(ns).Update(context.TODO(), d, metav1.UpdateOptions{})
	c.Kube.AppsV1().Deployments(ns).UpdateStatus(context.TODO(), d, metav1.UpdateOptions{})
	fakecluster.Eventually(t, func() bool { _, rd, _ := c.GetDeploymentStatus(ns, name); return rd == replicas }, name+" not updated")
}

func TestPassThroughBackendIsNeverWoken(t *testing.T) {
	h, c, tr := balancedHandler(t, sleeping("s1"), sleeping("s2"))

	// Everything asleep: only S1 is woken; the visitor waits.
	if w := visit(h); tr.last() != "" || !strings.Contains(w.Body.String(), "") {
		t.Fatalf("proxied while asleep: %s", tr.last())
	}
	if replicas(t, c, "s1") == 0 || replicas(t, c, "s2") != 0 {
		t.Fatalf("s1=%d s2=%d: only s1 must be woken", replicas(t, c, "s1"), replicas(t, c, "s2"))
	}

	// S1 ready, S2 still off: all traffic to S1, as the router would do.
	setReady(t, c, "s1", 1)
	for i := 0; i < 20; i++ {
		visit(h)
		if got := tr.last(); got != "s1" {
			t.Fatalf("request %d went to %q while s2 is off", i, got)
		}
	}
	if replicas(t, c, "s2") != 0 {
		t.Fatal("s2 woken")
	}
}

func TestBalancesWhenBothRunAndSticks(t *testing.T) {
	h, _, tr := balancedHandler(t, ready("s1", 1), ready("s2", 1))
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		visit(h)
		seen[tr.last()]++
	}
	if seen["s1"] < 60 || seen["s2"] < 60 {
		t.Fatalf("split = %v, want roughly 50/50", seen)
	}

	// A client keeps its backend.
	first := visit(h)
	var cookie *http.Cookie
	for _, c := range first.Result().Cookies() {
		if strings.HasPrefix(c.Name, "sp_backend_") {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no sticky cookie")
	}
	for i := 0; i < 20; i++ {
		visit(h, cookie)
		if tr.last() != cookie.Value {
			t.Fatalf("sticky client moved from %s to %s", cookie.Value, tr.last())
		}
	}
}

// Option (a): S1 asleep but S2 running: S2 answers right away while S1 wakes up.
func TestRunningPassThroughServesWhileManagedWakes(t *testing.T) {
	h, c, tr := balancedHandler(t, sleeping("s1"), ready("s2", 1))
	w := visit(h)
	if tr.last() != "s2" || w.Code != 200 {
		t.Fatalf("got %d via %q, want s2", w.Code, tr.last())
	}
	if replicas(t, c, "s1") == 0 {
		t.Fatal("s1 not woken")
	}
}
