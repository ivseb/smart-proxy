package k8s_test

import (
	"context"
	"reflect"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
)

func ingress(namespace, name, host, service string, port int32) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: host,
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{
					Path: "/",
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: service, Port: networkingv1.ServiceBackendPort{Number: port},
					}},
				}},
			}},
		}}},
	}
}

func route(namespace, name, host, service string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       routev1.RouteSpec{Host: host, To: routev1.RouteTargetReference{Kind: "Service", Name: service}},
	}
}

func namespace(name string, labelSet map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labelSet}}
}

func names[T metav1.Object](items []T) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.GetNamespace()+"/"+i.GetName())
	}
	return out
}

func TestParseScope(t *testing.T) {
	cases := []struct {
		watch, selector string
		want            string
		wantErr         bool
	}{
		{"", "", "namespaces own", false},
		{"b, a ,b", "", "namespaces a, b", false},
		{"*", "", "all namespaces", false},
		{"", "smart-proxy=enabled", `namespaces matching "smart-proxy=enabled"`, false},
		{"*", "team in (a,b)", `namespaces matching "team in (a,b)"`, false},
		{"a,b", "x=y", "", true},
		{"", "===", "", true},
	}
	for _, tc := range cases {
		scope, err := k8s.ParseScope(tc.watch, tc.selector, "own")
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseScope(%q, %q): err=%v", tc.watch, tc.selector, err)
			continue
		}
		if err == nil && scope.String() != tc.want {
			t.Errorf("ParseScope(%q, %q) = %q, want %q", tc.watch, tc.selector, scope.String(), tc.want)
		}
	}
}

func TestExplicitNamespaceList(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a", "team-b"}}, fakecluster.Options{},
		ingress("team-a", "web", "a.example.com", "web", 80),
		ingress("team-b", "web", "b.example.com", "web", 80),
		ingress("kube-system", "dashboard", "k.example.com", "dashboard", 80),
		route("team-b", "api", "api.example.com", "api"),
		route("other", "api", "other.example.com", "api"),
	)

	if got := c.WatchedNamespaces(); !reflect.DeepEqual(got, []string{"team-a", "team-b"}) {
		t.Fatalf("WatchedNamespaces = %v", got)
	}
	ings, err := c.ListIngresses()
	if err != nil || !reflect.DeepEqual(names(ings), []string{"team-a/web", "team-b/web"}) {
		t.Fatalf("ListIngresses = %v, %v", names(ings), err)
	}
	routes, err := c.ListRoutes()
	if err != nil || !reflect.DeepEqual(names(routes), []string{"team-b/api"}) {
		t.Fatalf("ListRoutes = %v, %v", names(routes), err)
	}
	if _, err := c.GetIngress("kube-system", "dashboard"); !k8s.NotWatchedError(err) {
		t.Fatalf("GetIngress outside scope: %v", err)
	}
	if c.DefaultNamespace() != "team-a" {
		t.Fatalf("DefaultNamespace = %q", c.DefaultNamespace())
	}

	// Returned objects are copies: changing them must not corrupt the cache.
	ing, _ := c.GetIngress("team-a", "web")
	ing.Spec.Rules[0].Host = "changed"
	again, _ := c.GetIngress("team-a", "web")
	if again.Spec.Rules[0].Host != "a.example.com" {
		t.Fatal("cache was modified through a returned object")
	}
}

func TestNamespaceSelectorFollowsLabels(t *testing.T) {
	selector, _ := labels.Parse("smart-proxy=enabled")
	c := fakecluster.New(t, k8s.Scope{All: true, Selector: selector}, fakecluster.Options{},
		namespace("team-a", map[string]string{"smart-proxy": "enabled"}),
		namespace("team-b", nil),
		ingress("team-a", "web", "a.example.com", "web", 80),
		ingress("team-b", "web", "b.example.com", "web", 80),
	)

	if !c.Watches("team-a") || c.Watches("team-b") {
		t.Fatalf("Watches: team-a=%v team-b=%v", c.Watches("team-a"), c.Watches("team-b"))
	}
	ings, _ := c.ListIngresses()
	if !reflect.DeepEqual(names(ings), []string{"team-a/web"}) {
		t.Fatalf("ListIngresses = %v", names(ings))
	}

	// Labelling a namespace brings it under management without a restart.
	nsB, _ := c.Kube.CoreV1().Namespaces().Get(context.TODO(), "team-b", metav1.GetOptions{})
	nsB.Labels = map[string]string{"smart-proxy": "enabled"}
	if _, err := c.Kube.CoreV1().Namespaces().Update(context.TODO(), nsB, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fakecluster.Eventually(t, func() bool { return c.Watches("team-b") }, "team-b not watched after labelling")
	if got := c.WatchedNamespaces(); !reflect.DeepEqual(got, []string{"team-a", "team-b"}) {
		t.Fatalf("WatchedNamespaces = %v", got)
	}
}

func TestVanillaKubernetesHasNoRoutes(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, fakecluster.Options{NoRoutesAPI: true})
	if c.RoutesEnabled() {
		t.Fatal("RoutesEnabled without the Routes API")
	}
	if routes, err := c.ListRoutes(); err != nil || len(routes) != 0 {
		t.Fatalf("ListRoutes = %v, %v", routes, err)
	}
}

func TestResolveServiceAndDeployment(t *testing.T) {
	dep := deployment(ns, "backend", 1)
	dep.Spec.Template.Labels = map[string]string{"app": "backend", "tier": "api"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "backend"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}, {Name: "metrics", Port: 9090}},
		},
	}
	c := cluster(t, fakecluster.Options{}, dep, svc)

	if name, _ := c.ResolveDeploymentForService(ns, "api"); name != "backend" {
		t.Errorf("ResolveDeploymentForService = %q, want backend (via selector)", name)
	}
	if name, port, err := c.ResolveServiceForDeployment(ns, "backend"); name != "api" || port != 8080 || err != nil {
		t.Errorf("ResolveServiceForDeployment = %q %d %v", name, port, err)
	}
	port, err := c.ResolveServicePort(ns, "api", &routev1.RoutePort{TargetPort: intstr.FromString("metrics")})
	if port != 9090 || err != nil {
		t.Errorf("ResolveServicePort(metrics) = %d %v", port, err)
	}
	if summary, ok := c.DeploymentForBackend(ns, "api"); !ok || summary.Name != "backend" || summary.Replicas != 1 {
		t.Errorf("DeploymentForBackend = %+v %v", summary, ok)
	}
}
