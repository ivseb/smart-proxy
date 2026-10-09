package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"smart-proxy/internal/logger"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AnnotationReplicasBeforeSleep records the replica count a Deployment had when Smart Proxy
// put it to sleep, so waking it restores the same size.
const AnnotationReplicasBeforeSleep = "smart-proxy/replicas-before-sleep"

// ErrManagedByKEDA is returned when a Deployment's replicas are owned by a KEDA ScaledObject.
// KEDA scales it back to its minReplicaCount, so Smart Proxy must not put it to sleep.
var ErrManagedByKEDA = errors.New("replicas are managed by a KEDA ScaledObject")

// SleepDeployment scales a Deployment to zero and remembers its replica count.
// It reports whether the Deployment was running (and is now asleep).
//
// A HorizontalPodAutoscaler is fine: Kubernetes disables an HPA whose target is at zero
// replicas until something scales it up again.
func (c *Client) SleepDeployment(namespace, name string) (bool, error) {
	ns := c.ns(namespace)
	hpa := c.lookupHPA(ns, name)
	if hpa != nil && IsKEDAManaged(hpa) {
		return false, ErrManagedByKEDA
	}

	dep, err := c.Clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas == 0 {
		return false, nil
	}

	// One patch for the annotation and the scale-down; resourceVersion makes it fail
	// instead of overwriting a concurrent change (e.g. a wake-up).
	err = c.patchDeployment(ns, name, map[string]any{
		"metadata": map[string]any{
			"resourceVersion": dep.ResourceVersion,
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
	hpa := c.lookupHPA(ns, name)

	for attempt := 0; attempt < 2; attempt++ {
		dep, err := c.Clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas > 0 {
			return 0, nil
		}

		target := WakeReplicas(dep.Annotations, hpa)
		err = c.patchDeployment(ns, name, map[string]any{
			"metadata": map[string]any{
				"resourceVersion": dep.ResourceVersion,
				"annotations":     map[string]any{AnnotationReplicasBeforeSleep: nil},
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
func (c *Client) FindHPA(namespace, deploymentName string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	list, err := c.Clientset.AutoscalingV2().HorizontalPodAutoscalers(c.ns(namespace)).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		ref := list.Items[i].Spec.ScaleTargetRef
		if ref.Kind == "Deployment" && ref.Name == deploymentName {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// lookupHPA is FindHPA for the sleep/wake paths: a failed lookup (typically RBAC without
// access to HPAs) must not stop scaling, so it is logged once and treated as "no HPA".
func (c *Client) lookupHPA(namespace, name string) *autoscalingv2.HorizontalPodAutoscaler {
	hpa, err := c.FindHPA(namespace, name)
	if err != nil {
		c.hpaWarnOnce.Do(func() {
			logger.Printf("Warning: cannot read HorizontalPodAutoscalers (%v); HPA/KEDA-aware scaling is disabled. Grant get/list on autoscaling/horizontalpodautoscalers.", err)
		})
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

func (c *Client) patchDeployment(namespace, name string, patch map[string]any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.Clientset.AppsV1().Deployments(namespace).Patch(context.TODO(), name, types.MergePatchType, data, metav1.PatchOptions{})
	return err
}

func (c *Client) ns(namespace string) string {
	if namespace == "" {
		return c.Namespace
	}
	return namespace
}
