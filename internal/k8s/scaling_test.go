package k8s_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
)

const ns = "apps"

func deployment(namespace, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
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

func cluster(t *testing.T, opts fakecluster.Options, objs ...runtime.Object) *fakecluster.Cluster {
	return fakecluster.New(t, k8s.Scope{Namespaces: []string{ns}}, opts, objs...)
}

func get(t *testing.T, c *fakecluster.Cluster, name string) *appsv1.Deployment {
	t.Helper()
	d, err := c.Kube.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSleepAndWakeRestoresReplicas(t *testing.T) {
	c := cluster(t, fakecluster.Options{}, deployment(ns, "web", 3))

	slept, err := c.SleepDeployment(ns, "web")
	if err != nil || !slept {
		t.Fatalf("sleep: slept=%v err=%v", slept, err)
	}
	d := get(t, c, "web")
	if *d.Spec.Replicas != 0 || d.Annotations[k8s.AnnotationReplicasBeforeSleep] != "3" {
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
	d = get(t, c, "web")
	if *d.Spec.Replicas != 3 {
		t.Fatalf("after wake: replicas=%d", *d.Spec.Replicas)
	}
	if _, ok := d.Annotations[k8s.AnnotationReplicasBeforeSleep]; ok {
		t.Fatalf("annotation not cleared: %v", d.Annotations)
	}

	// Waking a running deployment changes nothing.
	if target, err := c.WakeDeployment(ns, "web"); err != nil || target != 0 {
		t.Fatalf("second wake: target=%d err=%v", target, err)
	}
}

func TestWakeWithoutRecordDefaultsToOne(t *testing.T) {
	c := cluster(t, fakecluster.Options{}, deployment(ns, "manual", 0)) // scaled to zero by hand
	if target, err := c.WakeDeployment(ns, "manual"); err != nil || target != 1 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
}

func TestWakeWithHPAUsesMinReplicas(t *testing.T) {
	// Asleep after the HPA had scaled it to 8: the HPA, not the old peak, decides the size.
	d := deployment(ns, "api", 0)
	d.Annotations = map[string]string{k8s.AnnotationReplicasBeforeSleep: "8"}
	c := cluster(t, fakecluster.Options{}, d, hpaFor("api", 2), hpaFor("other", 5))

	if target, err := c.WakeDeployment(ns, "api"); err != nil || target != 2 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
}

func TestSleepSkipsKEDAManagedDeployments(t *testing.T) {
	hpa := hpaFor("worker", 1)
	hpa.Labels = map[string]string{"scaledobject.keda.sh/name": "worker"}
	c := cluster(t, fakecluster.Options{}, deployment(ns, "worker", 2), hpa)

	slept, err := c.SleepDeployment(ns, "worker")
	if !errors.Is(err, k8s.ErrManagedByKEDA) || slept {
		t.Fatalf("sleep: slept=%v err=%v", slept, err)
	}
	if r := *get(t, c, "worker").Spec.Replicas; r != 2 {
		t.Fatalf("replicas changed to %d", r)
	}
}

func TestScalingWorksWithoutHPAPermission(t *testing.T) {
	d := deployment(ns, "api", 0)
	d.Annotations = map[string]string{k8s.AnnotationReplicasBeforeSleep: "4"}
	c := cluster(t, fakecluster.Options{Deny: []string{"autoscaling/horizontalpodautoscalers"}}, d, hpaFor("api", 2))

	// The HPA can't be seen, so the recorded count is used.
	if target, err := c.WakeDeployment(ns, "api"); err != nil || target != 4 {
		t.Fatalf("wake: target=%d err=%v", target, err)
	}
}

func TestScalingRefusesUnwatchedNamespaces(t *testing.T) {
	c := cluster(t, fakecluster.Options{}, deployment("other", "web", 1))
	if _, err := c.SleepDeployment("other", "web"); !k8s.NotWatchedError(err) {
		t.Fatalf("sleep in unwatched namespace: %v", err)
	}
	if _, err := c.WakeDeployment("other", "web"); !k8s.NotWatchedError(err) {
		t.Fatalf("wake in unwatched namespace: %v", err)
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
		{"recorded", map[string]string{k8s.AnnotationReplicasBeforeSleep: "4"}, nil, 4},
		{"garbage recorded", map[string]string{k8s.AnnotationReplicasBeforeSleep: "lots"}, nil, 1},
		{"zero recorded", map[string]string{k8s.AnnotationReplicasBeforeSleep: "0"}, nil, 1},
		{"hpa min wins", map[string]string{k8s.AnnotationReplicasBeforeSleep: "4"}, hpaFor("x", 3), 3},
		{"hpa without min", nil, hpaNoMin, 1},
		{"hpa min zero", nil, hpaZeroMin, 1},
	}
	for _, tc := range cases {
		if got := k8s.WakeReplicas(tc.annotations, tc.hpa); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A workload woken by another replica stays up until that replica's activity reaches the leader.
func TestRecentlyWokenWorkloadsAreNotSleptAgain(t *testing.T) {
	c := cluster(t, fakecluster.Options{}, deployment(ns, "web", 0))
	if _, err := c.WakeDeployment(ns, "web"); err != nil {
		t.Fatal(err)
	}
	if slept, err := c.SleepIdleDeployment(ns, "web", time.Minute); slept || err != nil {
		t.Fatalf("slept a workload woken just now: %v %v", slept, err)
	}
	if slept, err := c.SleepDeployment(ns, "web"); !slept || err != nil {
		t.Fatalf("an explicit sleep must still work: %v %v", slept, err)
	}
}
