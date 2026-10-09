package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"
)

const proxyService = "sp"

func app(namespace, name string, replicas int32) []runtime.Object {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: replicas},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-svc", Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
		},
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: name + "." + namespace + ".example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: name + "-svc", Port: networkingv1.ServiceBackendPort{Number: 8080}},
				}}},
			}},
		}}},
	}
	return []runtime.Object{dep, svc, ing}
}

type fixture struct {
	cluster *fakecluster.Cluster
	store   *store.Store
	srv     *httptest.Server
}

func newFixture(t *testing.T, objs ...runtime.Object) *fixture {
	t.Helper()
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a", "team-b"}}, fakecluster.Options{}, objs...)
	st := store.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	authn, err := auth.New(auth.Config{Mode: auth.ModeNone, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(c.Client, st, proxy.NewMetrics(), proxyService, authn).Handler())
	t.Cleanup(srv.Close)
	return &fixture{cluster: c, store: st, srv: srv}
}

func (f *fixture) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = strings.NewReader(string(data))
	} else {
		reader = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, reader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(buf.String())
}

func (f *fixture) ingress(t *testing.T, ns, name string) *networkingv1.Ingress {
	t.Helper()
	ing, err := f.cluster.Kube.NetworkingV1().Ingresses(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ing
}

// waitCached waits until the informer cache sees what the API already has.
func (f *fixture) waitCached(t *testing.T, ns, name string, patched bool) {
	fakecluster.Eventually(t, func() bool {
		ing, err := f.cluster.GetIngress(ns, name)
		return err == nil && (ing.Annotations[k8s.AnnotationPatched] == "true") == patched
	}, "ingress cache not updated")
}

func TestListsResourcesAcrossNamespaces(t *testing.T) {
	objs := append(app("team-a", "web", 1), app("team-b", "web", 2)...)
	objs = append(objs, app("kube-system", "dashboard", 1)...)
	f := newFixture(t, objs...)

	code, body := f.call(t, "GET", "/api/k8s/ingresses", nil)
	var res []PatchableResource
	json.Unmarshal(body, &res)
	if code != 200 || len(res) != 2 {
		t.Fatalf("ingresses: %d %s", code, body)
	}
	if res[0].Namespace != "team-a" || res[1].Namespace != "team-b" || res[1].Deployment == nil || res[1].Deployment.Replicas != 2 {
		t.Fatalf("ingresses: %+v", res)
	}

	code, body = f.call(t, "GET", "/api/info", nil)
	if code != 200 || !strings.Contains(string(body), `"namespaces":["team-a","team-b"]`) {
		t.Fatalf("info: %d %s", code, body)
	}

	if code, _ := f.call(t, "GET", "/api/k8s/deployments?namespace=kube-system", nil); code != http.StatusForbidden {
		t.Fatalf("deployments outside scope: %d", code)
	}
}

func TestPatchAndUnpatchIngressPerNamespace(t *testing.T) {
	f := newFixture(t, append(app("team-a", "web", 1), app("team-b", "web", 1)...)...)

	if code, body := f.call(t, "POST", "/api/patch-ingress?namespace=team-b&name=web", nil); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	patched := f.ingress(t, "team-b", "web")
	if !k8s.IsIngressPatched(patched, proxyService) {
		t.Fatalf("team-b/web not patched: %+v", patched.Spec.Rules[0].HTTP.Paths[0].Backend)
	}
	if k8s.IsIngressPatched(f.ingress(t, "team-a", "web"), proxyService) {
		t.Fatal("team-a/web patched too (name collision across namespaces)")
	}

	route, ok := f.store.GetRoute(store.IngressID("team-b", "web"))
	if !ok || route.Namespace != "team-b" || route.Deployment != "web" || route.TargetService != "web-svc" || route.TargetPort != 8080 {
		t.Fatalf("stored route: %+v", route)
	}

	code, body := f.call(t, "GET", "/api/routes", nil)
	var routes []RouteStatus
	json.Unmarshal(body, &routes)
	if code != 200 || len(routes) != 1 || routes[0].Source == nil || routes[0].Source.Namespace != "team-b" ||
		routes[0].Status != StatusReady || routes[0].SleepsAt == nil {
		t.Fatalf("routes: %d %s", code, body)
	}

	f.waitCached(t, "team-b", "web", true)
	if code, body := f.call(t, "POST", "/api/unpatch-ingress?namespace=team-b&name=web", nil); code != 200 {
		t.Fatalf("unpatch: %d %s", code, body)
	}
	restored := f.ingress(t, "team-b", "web").Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if restored.Name != "web-svc" || restored.Port.Number != 8080 {
		t.Fatalf("restored backend: %+v", restored)
	}
	if _, ok := f.store.GetRoute(store.IngressID("team-b", "web")); ok {
		t.Fatal("route not removed after unpatch")
	}
}

func TestPatchRejectsUnwatchedNamespace(t *testing.T) {
	f := newFixture(t, app("kube-system", "dashboard", 1)...)
	if code, _ := f.call(t, "POST", "/api/patch-ingress?namespace=kube-system&name=dashboard", nil); code != http.StatusForbidden {
		t.Fatalf("patch outside scope: %d", code)
	}
}

func TestPatchOpenShiftRoute(t *testing.T) {
	rt := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a"},
		Spec: routev1.RouteSpec{
			Host: "api.example.com",
			To:   routev1.RouteTargetReference{Kind: "Service", Name: "web-svc"},
		},
	}
	f := newFixture(t, append(app("team-a", "web", 1), rt)...)

	if code, body := f.call(t, "POST", "/api/patch-route?namespace=team-a&name=api", nil); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	got, _ := f.cluster.Routes.RouteV1().Routes("team-a").Get(context.TODO(), "api", metav1.GetOptions{})
	if !k8s.IsRoutePatched(got, proxyService) {
		t.Fatalf("route not patched: %+v", got.Spec)
	}
	if r, ok := f.store.GetRoute(store.RouteID("team-a", "api")); !ok || r.TargetPort != 8080 || r.Deployment != "web" {
		t.Fatalf("stored route: %+v", r)
	}
}

func TestNewRouteBindsToIngressInItsNamespace(t *testing.T) {
	f := newFixture(t, append(app("team-a", "web", 1), app("team-b", "web", 1)...)...)

	cfg := store.RouteConfig{Host: "web.team-b.example.com", Namespace: "team-b", Deployment: "web", TargetService: "web-svc", TargetPort: 8080}
	if code, body := f.call(t, "POST", "/api/routes", cfg); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if _, ok := f.store.GetRoute(store.IngressID("team-b", "web")); !ok {
		t.Fatalf("route not bound to team-b/web: %+v", f.store.GetAllRoutes())
	}
	if !k8s.IsIngressPatched(f.ingress(t, "team-b", "web"), proxyService) {
		t.Fatal("ingress not auto-patched")
	}
	if k8s.IsIngressPatched(f.ingress(t, "team-a", "web"), proxyService) {
		t.Fatal("ingress in another namespace was patched")
	}

	cfg.Namespace = "kube-system"
	if code, _ := f.call(t, "POST", "/api/routes", cfg); code != http.StatusForbidden {
		t.Fatalf("create outside scope: %d", code)
	}
}

func TestWakeDeploymentRecordsActivity(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 0)...)
	f.store.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web"})
	f.store.SetActivityForTest(store.IngressID("team-a", "web"), time.Now().Add(-time.Hour))

	if code, body := f.call(t, "POST", "/api/k8s/wake-deployment?namespace=team-a&deployment=web", nil); code != 200 {
		t.Fatalf("wake: %d %s", code, body)
	}
	d, _ := f.cluster.Kube.AppsV1().Deployments("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if *d.Spec.Replicas != 1 {
		t.Fatalf("replicas = %d", *d.Spec.Replicas)
	}
	if r, _ := f.store.GetRoute(store.IngressID("team-a", "web")); time.Since(r.LastActivity) > time.Minute {
		t.Fatalf("activity not recorded: %v", r.LastActivity)
	}
}

func routeObj(ns, name, host, service string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       routev1.RouteSpec{Host: host, To: routev1.RouteTargetReference{Kind: "Service", Name: service}},
	}
}

func (f *fixture) osRoute(t *testing.T, ns, name string) *routev1.Route {
	t.Helper()
	rt, err := f.cluster.Routes.RouteV1().Routes(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// A route serving the hosts of two OpenShift Routes patches both; deleting it must restore both.
func TestDeletingMultiHostRouteRestoresEveryPatchedResource(t *testing.T) {
	objs := append(app("team-a", "web", 1),
		routeObj("team-a", "web-com", "web.example.com", "web-svc"),
		routeObj("team-a", "web-it", "web.example.it", "web-svc"))
	f := newFixture(t, objs...)

	cfg := store.RouteConfig{Host: "web.example.com, web.example.it", Namespace: "team-a", Deployment: "web", TargetService: "web-svc", TargetPort: 8080}
	if code, body := f.call(t, "POST", "/api/routes", cfg); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, name := range []string{"web-com", "web-it"} {
		if !k8s.IsRoutePatched(f.osRoute(t, "team-a", name), proxyService) {
			t.Fatalf("%s not patched", name)
		}
	}
	routes := f.store.GetAllRoutes()
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	waitPatched := func(name string, patched bool) {
		fakecluster.Eventually(t, func() bool {
			rt, err := f.cluster.GetRoute("team-a", name)
			return err == nil && k8s.IsRoutePatched(rt, proxyService) == patched
		}, name+" cache not updated")
	}
	waitPatched("web-com", true)
	waitPatched("web-it", true)

	if code, body := f.call(t, "DELETE", "/api/routes?id="+url.QueryEscape(routes[0].ID), nil); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	for _, name := range []string{"web-com", "web-it"} {
		rt := f.osRoute(t, "team-a", name)
		if rt.Spec.To.Name != "web-svc" || rt.Annotations[k8s.AnnotationPatched] != "" {
			t.Errorf("%s still patched after deleting its route: to=%s annotations=%v", name, rt.Spec.To.Name, rt.Annotations)
		}
	}
}

func multiHostFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	objs := append(app("team-a", "web", 1),
		routeObj("team-a", "web-com", "web.example.com", "web-svc"),
		routeObj("team-a", "web-it", "web.example.it", "web-svc"))
	f := newFixture(t, objs...)
	cfg := store.RouteConfig{Host: "web.example.com, web.example.it", Namespace: "team-a", Deployment: "web", TargetService: "web-svc", TargetPort: 8080}
	if code, body := f.call(t, "POST", "/api/routes", cfg); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, name := range []string{"web-com", "web-it"} {
		n := name
		fakecluster.Eventually(t, func() bool {
			rt, err := f.cluster.GetRoute("team-a", n)
			return err == nil && k8s.IsRoutePatched(rt, proxyService)
		}, n+" not patched in cache")
	}
	return f, f.store.GetAllRoutes()[0].ID
}

func TestUnpatchingASecondaryHostKeepsTheRouteWithoutIt(t *testing.T) {
	f, id := multiHostFixture(t)
	if code, body := f.call(t, "POST", "/api/unpatch-route?namespace=team-a&name=web-it", nil); code != 200 {
		t.Fatalf("unpatch: %d %s", code, body)
	}
	r, ok := f.store.GetRoute(id)
	if !ok || r.Host != "web.example.com" {
		t.Fatalf("route = %+v, %v (want host web.example.com only)", r, ok)
	}
	if !k8s.IsRoutePatched(f.osRoute(t, "team-a", "web-com"), proxyService) {
		t.Fatal("primary resource was unpatched too")
	}
}

func TestUnpatchingThePrimaryRebindsTheRoute(t *testing.T) {
	f, id := multiHostFixture(t)
	if id != store.RouteID("team-a", "web-com") {
		t.Fatalf("route bound to %s", id)
	}
	if code, body := f.call(t, "POST", "/api/unpatch-route?namespace=team-a&name=web-com", nil); code != 200 {
		t.Fatalf("unpatch: %d %s", code, body)
	}
	if _, ok := f.store.GetRoute(id); ok {
		t.Fatal("old route still stored")
	}
	r, ok := f.store.GetRoute(store.RouteID("team-a", "web-it"))
	if !ok || r.Host != "web.example.it" {
		t.Fatalf("route not rebound to web-it: %+v %v", r, ok)
	}
}

func TestRemovingAHostRestoresItsResource(t *testing.T) {
	f, id := multiHostFixture(t)
	r, _ := f.store.GetRoute(id)
	r.Host = "web.example.com"
	if code, body := f.call(t, "POST", "/api/routes", r); code != http.StatusCreated {
		t.Fatalf("save: %d %s", code, body)
	}
	if rt := f.osRoute(t, "team-a", "web-it"); rt.Spec.To.Name != "web-svc" {
		t.Fatalf("web-it still patched: %s", rt.Spec.To.Name)
	}
	if !k8s.IsRoutePatched(f.osRoute(t, "team-a", "web-com"), proxyService) {
		t.Fatal("web-com unpatched")
	}
}

func TestDeclarativeRoutesCannotBeDeletedFromTheAPI(t *testing.T) {
	f := newFixture(t)
	f.store.AddRoute(&store.RouteConfig{ID: store.IngressID("team-a", "web"), Namespace: "team-a", Deployment: "web", Declarative: true})
	if code, _ := f.call(t, "DELETE", "/api/routes?id="+url.QueryEscape(store.IngressID("team-a", "web")), nil); code != http.StatusConflict {
		t.Fatalf("delete declarative: %d", code)
	}
}
