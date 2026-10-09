package k8s

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

const ns = "apps"

func deployment(name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

func hpaFor(target string, minReplicas int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: target + "-hpa", Namespace: ns},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: target, APIVersion: "apps/v1"},
			MinReplicas:    &minReplicas,
			MaxReplicas:    10,
		},
	}
}

func newFakeClient(objs ...runtime.Object) *Client {
	return &Client{Clientset: fake.NewSimpleClientset(objs...), Namespace: ns}
}

func (c *Client) get(t *testing.T, name string) *appsv1.Deployment {
	t.Helper()
	d, err := c.Clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSleepAndWakeRestoresReplicas(t *testing.T) {
	c := newFakeClient(deployment("web", 3))

	slept, err := c.SleepDeployment(ns, "web")
	if err != nil || !slept {
		t.Fatalf("sleep: slept=%v err=%v", slept, err)
	}
	d := c.get(t, "web")
	if *d.Spec.Replicas != 0 || d.Annotations[AnnotationReplicasBeforeSleep] != "3" {
		t.Fatalf("after sleep: replicas=%d annotations=%v", *d.Spec.Replicas, d.Annotations)
	}

	// Sleeping an already sleeping deployment is a no-op that keeps the recorded count.
	if slept, err := c.SleepDeployment(ns, "web"); err != nil || slept {
		t.Fatalf("second sleep: slept=%v err=%v", slept, err)
	}

	target, err := c.WakeDeployment(ns, "web")
	if err != nil || target != 3 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
	d = c.get(t, "web")
	if *d.Spec.Replicas != 3 {
		t.Fatalf("after wake: replicas=%d", *d.Spec.Replicas)
	}
	if _, ok := d.Annotations[AnnotationReplicasBeforeSleep]; ok {
		t.Fatalf("annotation not cleared: %v", d.Annotations)
	}

	// Waking a running deployment changes nothing.
	if target, err := c.WakeDeployment(ns, "web"); err != nil || target != 0 {
		t.Fatalf("second wake: target=%d err=%v", target, err)
	}
}

func TestWakeWithoutRecordDefaultsToOne(t *testing.T) {
	c := newFakeClient(deployment("manual", 0)) // scaled to zero by hand, not by Smart Proxy
	if target, err := c.WakeDeployment(ns, "manual"); err != nil || target != 1 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
}

func TestWakeWithHPAUsesMinReplicas(t *testing.T) {
	// Asleep after the HPA had scaled it to 8: the HPA, not the old peak, decides the size.
	d := deployment("api", 0)
	d.Annotations = map[string]string{AnnotationReplicasBeforeSleep: "8"}
	c := newFakeClient(d, hpaFor("api", 2), hpaFor("other", 5))

	if target, err := c.WakeDeployment(ns, "api"); err != nil || target != 2 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
}

func TestSleepSkipsKEDAManagedDeployments(t *testing.T) {
	hpa := hpaFor("worker", 1)
	hpa.Labels = map[string]string{"scaledobject.keda.sh/name": "worker"}
	c := newFakeClient(deployment("worker", 2), hpa)

	slept, err := c.SleepDeployment(ns, "worker")
	if !errors.Is(err, ErrManagedByKEDA) || slept {
		t.Fatalf("sleep: slept=%v err=%v", slept, err)
	}
	if r := *c.get(t, "worker").Spec.Replicas; r != 2 {
		t.Fatalf("replicas changed to %d", r)
	}
}

func TestWakeReplicas(t *testing.T) {
	zero := int32(0)
	hpaNoMin := hpaFor("x", 0)
	hpaNoMin.Spec.MinReplicas = nil
	hpaZeroMin := hpaFor("x", 0)
	hpaZeroMin.Spec.MinReplicas = &zero

	cases := []struct {
		name        string
		annotations map[string]string
		hpa         *autoscalingv2.HorizontalPodAutoscaler
		want        int32
	}{
		{"nothing recorded", nil, nil, 1},
		{"recorded", map[string]string{AnnotationReplicasBeforeSleep: "4"}, nil, 4},
		{"garbage recorded", map[string]string{AnnotationReplicasBeforeSleep: "lots"}, nil, 1},
		{"zero recorded", map[string]string{AnnotationReplicasBeforeSleep: "0"}, nil, 1},
		{"hpa min wins", map[string]string{AnnotationReplicasBeforeSleep: "4"}, hpaFor("x", 3), 3},
		{"hpa without min", nil, hpaNoMin, 1},
		{"hpa min zero", nil, hpaZeroMin, 1},
	}
	for _, tc := range cases {
		if got := WakeReplicas(tc.annotations, tc.hpa); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
