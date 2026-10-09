package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Objects returned by these methods are copies, so callers may modify them.

// GetDeploymentStatus returns the desired and ready replicas of a workload (a Deployment name
// or "statefulset/<name>", see ParseWorkload), from the cache.
func (c *Client) GetDeploymentStatus(namespace, ref string) (int32, int32, error) {
	w, err := c.getWorkload(namespace, ref)
	if err != nil {
		return 0, 0, err
	}
	return w.replicas(), w.Ready, nil
}

// ListDeployments returns the workloads of a namespace as references: Deployment names, then
// "statefulset/<name>" for StatefulSets.
func (c *Client) ListDeployments(namespace string) ([]string, error) {
	list, err := c.listWorkloads(namespace)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(list))
	for _, w := range list {
		refs = append(refs, w.ref())
	}
	return refs, nil
}

// DeploymentSummary is the replica state shown next to patchable resources.
type DeploymentSummary struct {
	Name     string `json:"name"`
	Replicas int32  `json:"replicas"`
	Ready    int32  `json:"ready"`
}

// GetDeploymentProbePaths returns the HTTP paths of the Deployment's probes (readiness,
// liveness, startup). Requests to them must not count as user activity.
func (c *Client) GetDeploymentProbePaths(namespace, ref string) ([]string, error) {
	w, err := c.getWorkload(namespace, ref)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, container := range w.Containers {
		for _, probe := range []*corev1.Probe{container.ReadinessProbe, container.LivenessProbe, container.StartupProbe} {
			if probe != nil && probe.HTTPGet != nil && probe.HTTPGet.Path != "" && !seen[probe.HTTPGet.Path] {
				seen[probe.HTTPGet.Path] = true
				paths = append(paths, probe.HTTPGet.Path)
			}
		}
	}
	return paths, nil
}

// ListIngresses returns the Ingresses of every watched namespace.
func (c *Client) ListIngresses() ([]*networkingv1.Ingress, error) {
	var result []*networkingv1.Ingress
	for _, ns := range c.WatchedNamespaces() {
		lister, err := c.ingresses(ns)
		if err != nil {
			return nil, err
		}
		list, err := lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		for _, ing := range list {
			result = append(result, ing.DeepCopy())
		}
	}
	sortByNamespaceName(result, func(i *networkingv1.Ingress) metav1.Object { return i })
	return result, nil
}

// GetIngress returns a copy of an Ingress from the cache.
func (c *Client) GetIngress(namespace, name string) (*networkingv1.Ingress, error) {
	lister, err := c.ingresses(namespace)
	if err != nil {
		return nil, err
	}
	ing, err := lister.Get(name)
	if err != nil {
		return nil, err
	}
	return ing.DeepCopy(), nil
}

// UpdateIngress writes an Ingress to the API.
func (c *Client) UpdateIngress(ing *networkingv1.Ingress) error {
	if !c.Watches(ing.Namespace) {
		return fmt.Errorf("%w: %q", errNotWatched, ing.Namespace)
	}
	if c.standIns != nil && IsIngressPatched(ing, c.standIns.service) {
		if err := c.EnsureStandIn(ing.Namespace); err != nil {
			return err // Patched, it would lead nowhere
		}
	}
	_, err := c.Clientset.NetworkingV1().Ingresses(ing.Namespace).Update(context.TODO(), ing, metav1.UpdateOptions{})
	return err
}

// ListRoutes returns the OpenShift Routes of every watched namespace (none if Routes are unavailable).
func (c *Client) ListRoutes() ([]*routev1.Route, error) {
	if !c.RoutesEnabled() {
		return nil, nil
	}
	var result []*routev1.Route
	for _, ns := range c.WatchedNamespaces() {
		lister, err := c.routes(ns)
		if err != nil {
			return nil, err
		}
		list, err := lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		for _, rt := range list {
			result = append(result, rt.DeepCopy())
		}
	}
	sortByNamespaceName(result, func(r *routev1.Route) metav1.Object { return r })
	return result, nil
}

// GetRoute returns a copy of an OpenShift Route from the cache.
func (c *Client) GetRoute(namespace, name string) (*routev1.Route, error) {
	lister, err := c.routes(namespace)
	if err != nil {
		return nil, err
	}
	rt, err := lister.Get(name)
	if err != nil {
		return nil, err
	}
	return rt.DeepCopy(), nil
}

// UpdateRoute writes an OpenShift Route to the API.
func (c *Client) UpdateRoute(rt *routev1.Route) error {
	if !c.RoutesEnabled() {
		return fmt.Errorf("OpenShift Routes are not available")
	}
	if !c.Watches(rt.Namespace) {
		return fmt.Errorf("%w: %q", errNotWatched, rt.Namespace)
	}
	if c.standIns != nil && IsRoutePatched(rt, c.standIns.service) {
		if err := c.EnsureStandIn(rt.Namespace); err != nil {
			return err // Patched, it would lead nowhere
		}
	}
	_, err := c.RouteClientSet.RouteV1().Routes(rt.Namespace).Update(context.TODO(), rt, metav1.UpdateOptions{})
	return err
}

// ResolveDeploymentForService finds the workload behind a Service: a Deployment named like the
// Service (without a "-svc" suffix, or exactly), else the workload whose pods the Service
// selects (Deployments before StatefulSets). It falls back to the Service name.
func (c *Client) ResolveDeploymentForService(namespace, serviceName string) (string, error) {
	workloads, err := c.listWorkloads(namespace)
	if err != nil {
		return serviceName, err
	}
	byName := map[string]bool{}
	for _, w := range workloads {
		if w.Kind == KindDeployment {
			byName[w.Name] = true
		}
	}
	if trimmed, ok := strings.CutSuffix(serviceName, "-svc"); ok && byName[trimmed] {
		return trimmed, nil
	}
	if byName[serviceName] {
		return serviceName, nil
	}

	services, err := c.services(namespace)
	if err != nil {
		return serviceName, err
	}
	if svc, err := services.Get(serviceName); err == nil {
		for _, w := range workloads {
			if selects(svc, w.TemplateLabels) {
				return w.ref(), nil
			}
		}
	}
	return serviceName, nil
}

// ResolveServicePort finds the Service port to dial for a Route's port spec. A Route's
// targetPort refers to the endpoints: a name is the Service port's name, a number the
// container port, i.e. the Service port whose targetPort it is. Defaults to the first port.
func (c *Client) ResolveServicePort(namespace, serviceName string, routePort *routev1.RoutePort) (int, error) {
	services, err := c.services(namespace)
	if err != nil {
		return 0, err
	}
	svc, err := services.Get(serviceName)
	if err != nil {
		return 0, err
	}
	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", serviceName)
	}
	if routePort == nil || routePort.TargetPort.String() == "" {
		return int(svc.Spec.Ports[0].Port), nil
	}
	if routePort.TargetPort.Type == intstr.Int {
		for _, p := range svc.Spec.Ports {
			if p.TargetPort.IntValue() == int(routePort.TargetPort.IntVal) || (p.TargetPort.IntValue() == 0 && p.Port == routePort.TargetPort.IntVal) {
				return int(p.Port), nil
			}
		}
		return int(routePort.TargetPort.IntVal), nil
	}
	for _, p := range svc.Spec.Ports {
		if p.Name == routePort.TargetPort.StrVal {
			return int(p.Port), nil
		}
	}
	return int(svc.Spec.Ports[0].Port), nil
}

// ResolveServiceForDeployment finds the Service (and its first port) exposing a workload:
// "<name>-svc", a Service with the same name, or one selecting the workload's pods.
func (c *Client) ResolveServiceForDeployment(namespace, ref string) (string, int, error) {
	_, name := ParseWorkload(ref)
	services, err := c.services(namespace)
	if err != nil {
		return "", 0, err
	}
	for _, candidate := range []string{name + "-svc", name} {
		if svc, err := services.Get(candidate); err == nil && len(svc.Spec.Ports) > 0 {
			return svc.Name, int(svc.Spec.Ports[0].Port), nil
		}
	}

	if w, err := c.getWorkload(namespace, ref); err == nil {
		list, _ := services.List(labels.Everything())
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		for _, svc := range list {
			if len(svc.Spec.Ports) > 0 && selects(svc, w.TemplateLabels) {
				return svc.Name, int(svc.Spec.Ports[0].Port), nil
			}
		}
	}
	return "", 0, fmt.Errorf("could not find service for %s", ref)
}

// DeploymentForBackend resolves the Deployment behind a Service and returns its replica state.
func (c *Client) DeploymentForBackend(namespace, serviceName string) (*DeploymentSummary, bool) {
	name, err := c.ResolveDeploymentForService(namespace, serviceName)
	if err != nil {
		return nil, false
	}
	replicas, ready, err := c.GetDeploymentStatus(namespace, name)
	if err != nil {
		return nil, false
	}
	return &DeploymentSummary{Name: name, Replicas: replicas, Ready: ready}, true
}

// selects reports whether a Service's selector matches a workload's pod template labels.
func selects(svc *corev1.Service, podLabels map[string]string) bool {
	if len(svc.Spec.Selector) == 0 {
		return false
	}
	return labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(podLabels))
}

func sortByNamespaceName[T any](items []T, meta func(T) metav1.Object) {
	sort.Slice(items, func(i, j int) bool {
		a, b := meta(items[i]), meta(items[j])
		if a.GetNamespace() != b.GetNamespace() {
			return a.GetNamespace() < b.GetNamespace()
		}
		return a.GetName() < b.GetName()
	})
}

// SleepingWorkload is a workload Smart Proxy put to sleep.
type SleepingWorkload struct {
	Namespace string
	Ref       string // See ParseWorkload
	// Recorded is the replica count saved when it went to sleep.
	Recorded string
}

// SleepingDeployments returns the workloads Smart Proxy put to sleep (they carry the
// replicas-before-sleep annotation and are at zero replicas), in every watched namespace.
func (c *Client) SleepingDeployments() ([]SleepingWorkload, error) {
	var result []SleepingWorkload
	for _, ns := range c.WatchedNamespaces() {
		list, err := c.listWorkloads(ns)
		if err != nil {
			return nil, err
		}
		for _, w := range list {
			if recorded, ok := w.Annotations[AnnotationReplicasBeforeSleep]; ok && w.replicas() == 0 {
				result = append(result, SleepingWorkload{Namespace: ns, Ref: w.ref(), Recorded: recorded})
			}
		}
	}
	return result, nil
}

// RoutePortFor is the Route port that targets a Service port: by name when it has one (Routes
// name the endpoint port), else by the port its traffic goes to on the pods. Nil when the
// Service or port is unknown (the router then uses the first port).
func (c *Client) RoutePortFor(namespace, service string, port int) *routev1.RoutePort {
	services, err := c.services(namespace)
	if err != nil {
		return nil
	}
	svc, err := services.Get(service)
	if err != nil {
		return nil
	}
	for _, p := range svc.Spec.Ports {
		if int(p.Port) != port {
			continue
		}
		if p.Name != "" {
			return &routev1.RoutePort{TargetPort: intstr.FromString(p.Name)}
		}
		if p.TargetPort.IntValue() != 0 {
			return &routev1.RoutePort{TargetPort: intstr.FromInt(p.TargetPort.IntValue())}
		}
		return &routev1.RoutePort{TargetPort: intstr.FromInt(port)}
	}
	return nil
}
