package k8s_test

import (
	"reflect"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"smart-proxy/internal/k8s"
)

func TestIngressPatchRoundTrip(t *testing.T) {
	ing := ingress(ns, "web", "web.example.com", "web-svc", 8080)
	original, ok := k8s.IngressBackend(ing)
	if !ok {
		t.Fatal("no backend")
	}

	if err := k8s.PatchIngress(ing, "my-proxy", original, `{"id":"x"}`); err != nil {
		t.Fatal(err)
	}
	if !k8s.IsIngressPatched(ing, "my-proxy") || k8s.IsIngressPatched(ing, "other-proxy") {
		t.Fatal("IsIngressPatched")
	}
	backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if backend.Name != "my-proxy" || backend.Port.Name != k8s.ProxyPortName || backend.Port.Number != 0 {
		t.Fatalf("patched backend = %+v", backend)
	}
	if b, _ := k8s.OriginalIngressBackend(ing); b.Service != "web-svc" || b.Port != 8080 {
		t.Fatalf("OriginalIngressBackend = %+v", b)
	}

	if err := k8s.UnpatchIngress(ing); err != nil {
		t.Fatal(err)
	}
	backend = ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if backend.Name != "web-svc" || backend.Port.Number != 8080 || backend.Port.Name != "" {
		t.Fatalf("restored backend = %+v", backend)
	}
	if len(ing.Annotations) != 0 {
		t.Fatalf("annotations left: %v", ing.Annotations)
	}
}

func TestIngressPatchKeepsNamedPorts(t *testing.T) {
	ing := ingress(ns, "web", "web.example.com", "web-svc", 0)
	ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
	original, _ := k8s.IngressBackend(ing)

	k8s.PatchIngress(ing, "my-proxy", original, "")
	if ing.Annotations[k8s.AnnotationOriginalPort] != "http" {
		t.Fatalf("original-port = %q", ing.Annotations[k8s.AnnotationOriginalPort])
	}
	k8s.UnpatchIngress(ing)
	if port := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port; port.Name != "http" || port.Number != 0 {
		t.Fatalf("restored port = %+v", port)
	}
}

func TestIngressWithoutServiceBackendIsNotPatchable(t *testing.T) {
	ing := ingress(ns, "static", "s.example.com", "x", 80)
	ing.Spec.Rules[0].HTTP.Paths[0].Backend = networkingv1.IngressBackend{} // e.g. a resource backend
	if _, ok := k8s.IngressBackend(ing); ok {
		t.Fatal("backend found")
	}
	if err := k8s.PatchIngress(ing, "my-proxy", k8s.Backend{}, ""); err != k8s.ErrNoServiceBackend {
		t.Fatalf("PatchIngress = %v", err)
	}
}

func TestRoutePatchRoundTripKeepsTrafficSplit(t *testing.T) {
	weight := int32(80)
	rt := route(ns, "api", "api.example.com", "api-v1")
	rt.Spec.To.Weight = &weight
	rt.Spec.AlternateBackends = []routev1.RouteTargetReference{{Kind: "Service", Name: "api-v2", Weight: &weight}}
	rt.Spec.Port = &routev1.RoutePort{TargetPort: intstr.FromString("http")}
	before := rt.DeepCopy()

	k8s.PatchRoute(rt, "my-proxy", k8s.Backend{Service: "api-v1", PortName: "http"}, `{"id":"x"}`)
	if !k8s.IsRoutePatched(rt, "my-proxy") || rt.Spec.AlternateBackends != nil || rt.Spec.To.Weight != nil {
		t.Fatalf("patched route = %+v", rt.Spec)
	}
	if rt.Spec.Port.TargetPort.StrVal != k8s.ProxyPortName {
		t.Fatalf("target port = %v", rt.Spec.Port.TargetPort)
	}
	if k8s.OriginalRouteService(rt) != "api-v1" {
		t.Fatalf("OriginalRouteService = %q", k8s.OriginalRouteService(rt))
	}

	if err := k8s.UnpatchRoute(rt); err != nil {
		t.Fatal(err)
	}
	rt.Annotations = before.Annotations // both nil/empty
	if !reflect.DeepEqual(rt.Spec, before.Spec) {
		t.Fatalf("restored spec = %+v\nwant %+v", rt.Spec, before.Spec)
	}
}

func TestUnpatchRefusesUnpatchedResources(t *testing.T) {
	if err := k8s.UnpatchIngress(ingress(ns, "web", "h", "svc", 80)); err == nil {
		t.Error("UnpatchIngress on an unpatched Ingress")
	}
	if err := k8s.UnpatchRoute(route(ns, "api", "h", "svc")); err == nil {
		t.Error("UnpatchRoute on an unpatched Route")
	}
}
