package k8s_test

import (
	"context"
	"testing"

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
	if err := c.EnableStandIns(ctx, "sp", "sp"); err != nil {
		t.Fatal(err)
	}

	ing, _ := c.GetIngress(ns, "web")
	original, _ := k8s.IngressBackend(ing)
	k8s.PatchIngress(ing, "sp", original, "{}")
	if err := c.UpdateIngress(ing); err != nil {
		t.Fatal(err)
	}
	ep, err := c.Kube.CoreV1().Endpoints(ns).Get(ctx, "sp", metav1.GetOptions{})
	if err != nil || len(ep.Subsets) != 1 || len(ep.Subsets[0].Addresses) != 2 || ep.Subsets[0].Ports[0].Port != 8080 || ep.Subsets[0].Ports[0].Name != "proxy" {
		t.Fatalf("stand-in endpoints = %+v, %v", ep, err)
	}
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
	if n, err := c.RemoveStandIns("sp", "sp"); n != 1 || err != nil {
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
	c.EnableStandIns(ctx, "sp", "sp")
	ing, _ := c.GetIngress(ns, "web")
	original, _ := k8s.IngressBackend(ing)
	k8s.PatchIngress(ing, "sp", original, "{}")
	if err := c.UpdateIngress(ing); err == nil {
		t.Fatal("patched an Ingress towards a Service that isn't Smart Proxy's")
	}
}
