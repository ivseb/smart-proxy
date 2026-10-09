package restore

import (
	"context"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
)

func TestRestoreUndoesEverything(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "web.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: "web-svc", Port: networkingv1.ServiceBackendPort{Number: 8080}},
				}}},
			}},
		}}},
	}
	k8s.PatchIngress(ing, "sp", k8s.Backend{Service: "web-svc", Port: 8080}, `{}`)

	rt := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-b"},
		Spec:       routev1.RouteSpec{Host: "api.example.com", To: routev1.RouteTargetReference{Kind: "Service", Name: "api-svc"}},
	}
	k8s.PatchRoute(rt, "sp", k8s.Backend{Service: "api-svc", Port: 80}, `{}`)

	zero := int32(0)
	asleep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a", Annotations: map[string]string{k8s.AnnotationReplicasBeforeSleep: "3"}},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	}
	// Scaled to zero by someone else: not Smart Proxy's to wake.
	manual := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "team-a"}, Spec: appsv1.DeploymentSpec{Replicas: &zero}}

	// Patched by another installation of Smart Proxy: not this one's to restore.
	foreign := ing.DeepCopy()
	foreign.Name = "foreign"
	k8s.UnpatchIngress(foreign)
	k8s.PatchIngress(foreign, "other-sp", k8s.Backend{Service: "web-svc", Port: 8080}, `{}`)

	c := fakecluster.New(t, k8s.Scope{Namespaces: []string{"team-a", "team-b"}}, fakecluster.Options{}, ing, rt, asleep, manual, foreign)

	res := Run(c.Client, "sp")
	if res.Err() != nil || res.Ingresses != 1 || res.Routes != 1 || res.Deployments != 1 {
		t.Fatalf("result = %+v", res)
	}

	gotIng, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "web", metav1.GetOptions{})
	if b := gotIng.Spec.Rules[0].HTTP.Paths[0].Backend.Service; b.Name != "web-svc" || b.Port.Number != 8080 || len(gotIng.Annotations) != 0 {
		t.Errorf("ingress not restored: %+v %v", b, gotIng.Annotations)
	}
	gotRt, _ := c.Routes.RouteV1().Routes("team-b").Get(context.TODO(), "api", metav1.GetOptions{})
	if gotRt.Spec.To.Name != "api-svc" || gotRt.Annotations[k8s.AnnotationPatched] != "" {
		t.Errorf("route not restored: %+v", gotRt.Spec)
	}
	for name, want := range map[string]int32{"web": 3, "batch": 0} {
		d, _ := c.Kube.AppsV1().Deployments("team-a").Get(context.TODO(), name, metav1.GetOptions{})
		if *d.Spec.Replicas != want {
			t.Errorf("%s: replicas = %d, want %d", name, *d.Spec.Replicas, want)
		}
	}

	got, _ := c.Kube.NetworkingV1().Ingresses("team-a").Get(context.TODO(), "foreign", metav1.GetOptions{})
	if got.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != "other-sp" {
		t.Error("restored an Ingress patched by another installation")
	}
}
