package k8s_test

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
)

func statefulSet(name string, replicas int32, labels map[string]string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}},
		},
	}
}

func TestParseWorkload(t *testing.T) {
	for ref, want := range map[string][2]string{
		"web":             {k8s.KindDeployment, "web"},
		"deployment/web":  {k8s.KindDeployment, "web"},
		"statefulset/db":  {k8s.KindStatefulSet, "db"},
		"StatefulSets/db": {k8s.KindStatefulSet, "db"},
		"sts/db":          {k8s.KindStatefulSet, "db"},
	} {
		kind, name := k8s.ParseWorkload(ref)
		if kind != want[0] || name != want[1] {
			t.Errorf("ParseWorkload(%q) = %s, %s", ref, kind, name)
		}
	}
	if k8s.WorkloadRef(k8s.KindStatefulSet, "db") != "statefulset/db" || k8s.WorkloadRef(k8s.KindDeployment, "web") != "web" {
		t.Error("WorkloadRef")
	}
}

func TestStatefulSetSleepWakeAndDiscovery(t *testing.T) {
	db := statefulSet("db", 3, map[string]string{"app": "postgres"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "postgres"}, Ports: []corev1.ServicePort{{Port: 5432}}},
	}
	hpa := hpaFor("db", 2)
	hpa.Spec.ScaleTargetRef.Kind = k8s.KindStatefulSet
	c := cluster(t, fakecluster.Options{}, db, svc, deployment(ns, "web", 1), hpa)

	if refs, _ := c.ListDeployments(ns); !reflect.DeepEqual(refs, []string{"web", "statefulset/db"}) {
		t.Fatalf("ListDeployments = %v", refs)
	}
	if ref, _ := c.ResolveDeploymentForService(ns, "postgres"); ref != "statefulset/db" {
		t.Fatalf("ResolveDeploymentForService = %q", ref)
	}
	if name, port, err := c.ResolveServiceForDeployment(ns, "statefulset/db"); name != "postgres" || port != 5432 || err != nil {
		t.Fatalf("ResolveServiceForDeployment = %q %d %v", name, port, err)
	}

	if slept, err := c.SleepDeployment(ns, "statefulset/db"); !slept || err != nil {
		t.Fatalf("sleep: %v %v", slept, err)
	}
	got, _ := c.Kube.AppsV1().StatefulSets(ns).Get(context.TODO(), "db", metav1.GetOptions{})
	if *got.Spec.Replicas != 0 || got.Annotations[k8s.AnnotationReplicasBeforeSleep] != "3" {
		t.Fatalf("after sleep: %d %v", *got.Spec.Replicas, got.Annotations)
	}
	fakecluster.Eventually(t, func() bool {
		s, _ := c.SleepingDeployments()
		return len(s) == 1 && s[0].Ref == "statefulset/db" && s[0].Recorded == "3"
	}, "sleeping StatefulSet not listed")

	// The HPA targeting the StatefulSet sets the wake-up size.
	if target, err := c.WakeDeployment(ns, "statefulset/db"); target != 2 || err != nil {
		t.Fatalf("wake: %d %v", target, err)
	}
	// The Deployment named like the StatefulSet's ref must not be touched.
	if d, _ := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), "web", metav1.GetOptions{}); *d.Spec.Replicas != 1 {
		t.Fatal("deployment changed")
	}
}
