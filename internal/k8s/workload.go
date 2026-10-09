package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	appsv1openshift "github.com/openshift/api/apps/v1"
)

// Workload kinds Smart Proxy can scale.
const (
	KindDeployment       = "Deployment"
	KindStatefulSet      = "StatefulSet"
	KindDeploymentConfig = "DeploymentConfig" // OpenShift apps.openshift.io/v1
)

// ParseWorkload splits a workload reference: "web" or "deployment/web" is a Deployment,
// "statefulset/db" a StatefulSet and "deploymentconfig/legacy" (or "dc/legacy") an OpenShift
// DeploymentConfig (the kubectl syntax). Plain names stay Deployments, so existing
// configurations keep working.
func ParseWorkload(ref string) (kind, name string) {
	prefix, rest, found := strings.Cut(ref, "/")
	if !found {
		return KindDeployment, ref
	}
	switch strings.ToLower(prefix) {
	case "statefulset", "statefulsets", "sts":
		return KindStatefulSet, rest
	case "deploymentconfig", "deploymentconfigs", "dc":
		return KindDeploymentConfig, rest
	default:
		return KindDeployment, rest
	}
}

// WorkloadRef is the reference for a workload: its name for a Deployment, "statefulset/<name>"
// for a StatefulSet.
func WorkloadRef(kind, name string) string {
	switch kind {
	case KindStatefulSet:
		return "statefulset/" + name
	case KindDeploymentConfig:
		return "deploymentconfig/" + name
	default:
		return name
	}
}

// workload is what Smart Proxy needs from a Deployment or StatefulSet.
type workload struct {
	Kind, Namespace, Name string
	Replicas              *int32
	Ready                 int32
	Annotations           map[string]string
	TemplateLabels        map[string]string
	Containers            []corev1.Container
	ResourceVersion       string
}

func (w *workload) ref() string { return WorkloadRef(w.Kind, w.Name) }

func (w *workload) replicas() int32 {
	if w.Replicas == nil {
		return 1 // Kubernetes' default
	}
	return *w.Replicas
}

func fromDeployment(d *appsv1.Deployment) *workload {
	return &workload{
		Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name,
		Replicas: d.Spec.Replicas, Ready: d.Status.ReadyReplicas, Annotations: d.Annotations,
		TemplateLabels: d.Spec.Template.Labels, Containers: d.Spec.Template.Spec.Containers,
		ResourceVersion: d.ResourceVersion,
	}
}

func fromStatefulSet(s *appsv1.StatefulSet) *workload {
	return &workload{
		Kind: KindStatefulSet, Namespace: s.Namespace, Name: s.Name,
		Replicas: s.Spec.Replicas, Ready: s.Status.ReadyReplicas, Annotations: s.Annotations,
		TemplateLabels: s.Spec.Template.Labels, Containers: s.Spec.Template.Spec.Containers,
		ResourceVersion: s.ResourceVersion,
	}
}

func fromDeploymentConfig(dc *appsv1openshift.DeploymentConfig) *workload {
	replicas := dc.Spec.Replicas
	w := &workload{
		Kind: KindDeploymentConfig, Namespace: dc.Namespace, Name: dc.Name,
		Replicas: &replicas, Ready: dc.Status.ReadyReplicas, Annotations: dc.Annotations,
		ResourceVersion: dc.ResourceVersion,
	}
	if dc.Spec.Template != nil {
		w.TemplateLabels, w.Containers = dc.Spec.Template.Labels, dc.Spec.Template.Spec.Containers
	}
	return w
}

// getWorkload reads a workload from the cache.
func (c *Client) getWorkload(namespace, ref string) (*workload, error) {
	kind, name := ParseWorkload(ref)
	if kind == KindDeploymentConfig {
		lister, err := c.deploymentConfigs(namespace)
		if err != nil {
			return nil, err
		}
		dc, err := lister.Get(name)
		if err != nil {
			return nil, err
		}
		return fromDeploymentConfig(dc), nil
	}
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	if kind == KindStatefulSet {
		s, err := f.Apps().V1().StatefulSets().Lister().StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return fromStatefulSet(s), nil
	}
	d, err := f.Apps().V1().Deployments().Lister().Deployments(namespace).Get(name)
	if err != nil {
		return nil, err
	}
	return fromDeployment(d), nil
}

// apiTimeout bounds calls to the API server made while serving requests: a slow API server
// must not hold requests (and goroutines) forever.
const apiTimeout = 10 * time.Second

func apiContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), apiTimeout)
}

// fetchWorkload reads a workload from the API (fresh, with its current resourceVersion).
func (c *Client) fetchWorkload(namespace, ref string) (*workload, error) {
	ctx, cancel := apiContext()
	defer cancel()
	kind, name := ParseWorkload(ref)
	if kind == KindDeploymentConfig {
		if c.AppsClientSet == nil {
			return nil, fmt.Errorf("OpenShift DeploymentConfigs are not available")
		}
		dc, err := c.AppsClientSet.AppsV1().DeploymentConfigs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return fromDeploymentConfig(dc), nil
	}
	if kind == KindStatefulSet {
		s, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return fromStatefulSet(s), nil
	}
	d, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromDeployment(d), nil
}

// patchWorkload applies a JSON merge patch to a workload.
func (c *Client) patchWorkload(namespace, ref string, patch map[string]any) error {
	ctx, cancel := apiContext()
	defer cancel()
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	kind, name := ParseWorkload(ref)
	switch kind {
	case KindStatefulSet:
		_, err = c.Clientset.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{})
	case KindDeploymentConfig:
		if c.AppsClientSet == nil {
			return fmt.Errorf("OpenShift DeploymentConfigs are not available")
		}
		_, err = c.AppsClientSet.AppsV1().DeploymentConfigs(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{})
	default:
		_, err = c.Clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{})
	}
	return err
}

// listWorkloads returns the Deployments, StatefulSets and DeploymentConfigs of a namespace from
// the cache, in that order, each sorted by name.
func (c *Client) listWorkloads(namespace string) ([]*workload, error) {
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	deps, err := f.Apps().V1().Deployments().Lister().Deployments(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	sets, err := f.Apps().V1().StatefulSets().Lister().StatefulSets(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	result := make([]*workload, 0, len(deps)+len(sets))
	for _, d := range deps {
		result = append(result, fromDeployment(d))
	}
	for _, s := range sets {
		result = append(result, fromStatefulSet(s))
	}
	if c.DeploymentConfigsEnabled() {
		lister, err := c.deploymentConfigs(namespace)
		if err != nil {
			return nil, err
		}
		dcs, err := lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		sort.Slice(dcs, func(i, j int) bool { return dcs[i].Name < dcs[j].Name })
		for _, dc := range dcs {
			result = append(result, fromDeploymentConfig(dc))
		}
	}
	return result, nil
}
