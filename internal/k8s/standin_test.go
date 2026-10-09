package k8s_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
)

func proxyEndpoints(ips ...string) *corev1.Endpoints {
	ep := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "sp", Namespace: "smart-proxy"}}
	subset := corev1.EndpointSubset{Ports: []corev1.EndpointPort{{Name: "proxy", Port: 8080}, {Name: "admin", Port: 8081}}}
	for _, ip := range ips {
		subset.Addresses = append(subset.Addresses, corev1.EndpointAddress{IP: ip})
	}
	ep.Subsets = []corev1.EndpointSubset{subset}
	return ep
}

// An Ingress patched in another namespace than Smart Proxy's ("smart-proxy", the first of the
// scope) must reach Smart Proxy's pods.
func TestPatchedResourcesElsewhereGetAStandIn(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"smart-proxy", ns}}, fakecluster.Options{},
		ingress(ns, "web", "web.example.com", "web-svc", 80), proxyEndpoints("10.0.0.1", "10.0.0.2"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.EnableStandIns(ctx, "sp")
	c.SetLeading(true)

	ing, _ := c.GetIngress(ns, "web")
	original, _ := k8s.IngressBackend(ing)
	k8s.PatchIngress(ing, "sp", original, "{}")
	if err := c.UpdateIngress(ing); err != nil {
		t.Fatal(err)
	}
	fakecluster.Eventually(t, func() bool {
		ep, err := c.Kube.CoreV1().Endpoints(ns).Get(ctx, "sp", metav1.GetOptions{})
		return err == nil && len(ep.Subsets) == 1 && len(ep.Subsets[0].Addresses) == 2 && ep.Subsets[0].Ports[0].Port == 8080 && ep.Subsets[0].Ports[0].Name == "proxy"
	}, "stand-in endpoints not mirrored")
	if svc, err := c.Kube.CoreV1().Services(ns).Get(ctx, "sp", metav1.GetOptions{}); err != nil || svc.Spec.Selector != nil || svc.Spec.Ports[0].Name != "proxy" {
		t.Fatalf("stand-in Service = %+v, %v", svc, err)
	}

	// Smart Proxy's pods change (rolling update): the stand-in follows.
	c.Kube.CoreV1().Endpoints("smart-proxy").Update(ctx, proxyEndpoints("10.0.0.3"), metav1.UpdateOptions{})
	fakecluster.Eventually(t, func() bool {
		ep, _ := c.Kube.CoreV1().Endpoints(ns).Get(ctx, "sp", metav1.GetOptions{})
		return len(ep.Subsets) == 1 && len(ep.Subsets[0].Addresses) == 1 && ep.Subsets[0].Addresses[0].IP == "10.0.0.3"
	}, "stand-in not updated")

	// Restored: the stand-in goes.
	fakecluster.Eventually(t, func() bool { _, err := c.GetIngress(ns, "web"); return err == nil }, "cache")
	if n, err := c.RemoveStandIns("sp"); n != 1 || err != nil {
		t.Fatalf("RemoveStandIns = %d, %v", n, err)
	}
	if _, err := c.Kube.CoreV1().Services(ns).Get(ctx, "sp", metav1.GetOptions{}); err == nil {
		t.Fatal("stand-in left behind")
	}
}

func TestStandInNeverReplacesSomeoneElsesService(t *testing.T) {
	theirs := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "sp", Namespace: ns}}
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"smart-proxy", ns}}, fakecluster.Options{},
		ingress(ns, "web", "web.example.com", "web-svc", 80), proxyEndpoints("10.0.0.1"), theirs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.EnableStandIns(ctx, "sp")
	ing, _ := c.GetIngress(ns, "web")
	original, _ := k8s.IngressBackend(ing)
	k8s.PatchIngress(ing, "sp", original, "{}")
	if err := c.UpdateIngress(ing); err == nil {
		t.Fatal("patched an Ingress towards a Service that isn't Smart Proxy's")
	}
}

// Upgraded without the new RBAC: Smart Proxy still starts, and refuses to patch where it
// couldn't be reached instead of breaking the application.
func TestWithoutEndpointsPermissionPatchingElsewhereIsRefused(t *testing.T) {
	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"smart-proxy", ns}}, fakecluster.Options{Deny: []string{"/endpoints"}},
		ingress(ns, "web", "web.example.com", "web-svc", 80), ingress("smart-proxy", "own", "own.example.com", "own-svc", 80))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.EnableStandIns(ctx, "sp")
	if ctx.Err() != nil {
		t.Fatal("startup waited for endpoints it may not read")
	}
	ing, _ := c.GetIngress(ns, "web")
	original, _ := k8s.IngressBackend(ing)
	k8s.PatchIngress(ing, "sp", original, "{}")
	if err := c.UpdateIngress(ing); err == nil {
		t.Fatal("patched an Ingress that couldn't reach Smart Proxy")
	}
	own, _ := c.GetIngress("smart-proxy", "own")
	original, _ = k8s.IngressBackend(own)
	k8s.PatchIngress(own, "sp", original, "{}")
	if err := c.UpdateIngress(own); err != nil {
		t.Fatalf("patching in Smart Proxy's own namespace: %v", err)
	}
}
