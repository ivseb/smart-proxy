package k8s

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"smart-proxy/internal/logger"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
)

// AnnotationReplicasBeforeSleep records the replica count a Deployment had when Smart Proxy
// put it to sleep, so waking it restores the same size.
const AnnotationReplicasBeforeSleep = "smart-proxy/replicas-before-sleep"

// AnnotationWokenAt records when Smart Proxy last woke a workload (RFC 3339). A replica that
// isn't the leader wakes workloads too, and the leader learns about that request's activity only
// some seconds later: until then the workload must not be put back to sleep.
const AnnotationWokenAt = "smart-proxy/woken-at"

// ErrManagedByKEDA is returned when a Deployment's replicas are owned by a KEDA ScaledObject.
// KEDA scales it back to its minReplicaCount, so Smart Proxy must not put it to sleep.
var ErrManagedByKEDA = errors.New("replicas are managed by a KEDA ScaledObject")

// SleepDeployment scales a Deployment to zero and remembers its replica count.
// It reports whether the Deployment was running (and is now asleep).
//
// A HorizontalPodAutoscaler is fine: Kubernetes disables an HPA whose target is at zero
// replicas until something scales it up again.
func (c *Client) SleepDeployment(namespace, name string) (bool, error) {
	return c.SleepIdleDeployment(namespace, name, 0)
}

// SleepIdleDeployment is SleepDeployment, except for a workload Smart Proxy woke less than
// minAwake ago: it is left running.
func (c *Client) SleepIdleDeployment(namespace, name string, minAwake time.Duration) (bool, error) {
	ns := c.ns(namespace)
	if !c.Watches(ns) {
		return false, fmt.Errorf("%w: %q", errNotWatched, ns)
	}
	hpa := c.lookupHPA(ns, name)
	if hpa != nil && IsKEDAManaged(hpa) {
		return false, ErrManagedByKEDA
	}

	w, err := c.fetchWorkload(ns, name)
	if err != nil {
		return false, err
	}
	replicas := w.replicas()
	if replicas == 0 {
		return false, nil
	}
	if woken, err := time.Parse(time.RFC3339, w.Annotations[AnnotationWokenAt]); err == nil && time.Since(woken) < minAwake {
		return false, nil
	}

	// One patch for the annotation and the scale-down; resourceVersion makes it fail
	// instead of overwriting a concurrent change (e.g. a wake-up).
	err = c.patchWorkload(ns, name, map[string]any{
		"metadata": map[string]any{
			"resourceVersion": w.ResourceVersion,
			"annotations":     map[string]any{AnnotationReplicasBeforeSleep: strconv.Itoa(int(replicas))},
		},
		"spec": map[string]any{"replicas": 0},
	})
	return err == nil, err
}

// WakeDeployment scales a sleeping Deployment back up and returns the replica count it asked for
// (0 if it was already running). The size is, in order of preference:
//   - the HPA's minReplicas, when an HPA manages it (the HPA then takes over);
//   - the replica count recorded when it was put to sleep;
//   - 1.
func (c *Client) WakeDeployment(namespace, name string) (int32, error) {
	ns := c.ns(namespace)
	if !c.Watches(ns) {
		return 0, fmt.Errorf("%w: %q", errNotWatched, ns)
	}
	hpa := c.lookupHPA(ns, name)

	for attempt := 0; attempt < 2; attempt++ {
		w, err := c.fetchWorkload(ns, name)
		if err != nil {
			return 0, err
		}
		if w.replicas() > 0 {
			return 0, nil
		}

		target := WakeReplicas(w.Annotations, hpa)
		err = c.patchWorkload(ns, name, map[string]any{
			"metadata": map[string]any{
				"resourceVersion": w.ResourceVersion,
				"annotations": map[string]any{
					AnnotationReplicasBeforeSleep: nil,
					AnnotationWokenAt:             time.Now().UTC().Format(time.RFC3339),
				},
			},
			"spec": map[string]any{"replicas": target},
		})
		if apierrors.IsConflict(err) {
			continue // Changed meanwhile, typically a concurrent request waking it: re-check.
		}
		if err != nil {
			return 0, err
		}
		return target, nil
	}
	return 0, nil
}

// WakeReplicas picks the replica count to wake a Deployment with; see WakeDeployment.
func WakeReplicas(annotations map[string]string, hpa *autoscalingv2.HorizontalPodAutoscaler) int32 {
	if hpa != nil {
		if hpa.Spec.MinReplicas != nil && *hpa.Spec.MinReplicas > 0 {
			return *hpa.Spec.MinReplicas
		}
		return 1
	}
	if n, err := strconv.Atoi(annotations[AnnotationReplicasBeforeSleep]); err == nil && n > 0 {
		return int32(n)
	}
	return 1
}

// FindHPA returns the HorizontalPodAutoscaler targeting the Deployment, or nil if there is none.
func (c *Client) FindHPA(namespace, ref string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	kind, name := ParseWorkload(ref)
	lister, err := c.hpas(namespace)
	if err != nil {
		return nil, err
	}
	list, err := lister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, hpa := range list {
		if target := hpa.Spec.ScaleTargetRef; target.Kind == kind && target.Name == name {
			return hpa.DeepCopy(), nil
		}
	}
	return nil, nil
}

// lookupHPA is FindHPA for the sleep/wake paths: a failed lookup must not stop scaling,
// so it is treated as "no HPA". Missing RBAC was already reported when the caches started.
func (c *Client) lookupHPA(namespace, name string) *autoscalingv2.HorizontalPodAutoscaler {
	hpa, err := c.FindHPA(namespace, name)
	if err != nil {
		if c.cache != nil && c.cache.hpaEnabled {
			c.hpaWarnOnce.Do(func() {
				logger.Printf("Warning: cannot read HorizontalPodAutoscalers (%v); HPA/KEDA-aware scaling is disabled.", err)
			})
		}
		return nil
	}
	return hpa
}

// IsKEDAManaged reports whether an HPA was created by a KEDA ScaledObject.
func IsKEDAManaged(hpa *autoscalingv2.HorizontalPodAutoscaler) bool {
	if _, ok := hpa.Labels["scaledobject.keda.sh/name"]; ok {
		return true
	}
	for _, owner := range hpa.OwnerReferences {
		if owner.Kind == "ScaledObject" {
			return true
		}
	}
	return false
}

func (c *Client) ns(namespace string) string {
	if namespace == "" {
		return c.DefaultNamespace()
	}
	return namespace
}
