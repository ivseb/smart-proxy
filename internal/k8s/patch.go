package k8s

import (
	"encoding/json"
	"errors"
	"strconv"

	routev1 "github.com/openshift/api/route/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Annotations Smart Proxy keeps on the Ingresses and Routes it patches.
const (
	AnnotationPatched          = "smart-proxy/patched"
	AnnotationOriginalService  = "smart-proxy/original-service"
	AnnotationOriginalPort     = "smart-proxy/original-port"
	AnnotationOriginalBackends = "smart-proxy/original-backends"
	AnnotationConfig           = "smart-proxy/config"
	// AnnotationDeclarative marks resources patched because their own annotations asked for it
	// (smart-proxy/enabled), as opposed to from the dashboard.
	AnnotationDeclarative = "smart-proxy/declarative"
)

// LegacyServiceName is the Service name older versions always patched resources to,
// regardless of the actual Service name.
const LegacyServiceName = "smart-proxy"

// IsProxyService reports whether a backend Service name points at Smart Proxy itself,
// including resources patched by older versions under the legacy name.
func IsProxyService(name, proxyService string) bool {
	return name == proxyService || name == LegacyServiceName
}

// Backend is an application Service and port, as found before patching.
type Backend struct {
	Service string
	// Port is the Service port number, or 0 when it is referenced by PortName.
	Port     int
	PortName string
}

// PortString is the port as stored in the original-port annotation: a number or a name.
func (b Backend) PortString() string {
	if b.Port == 0 && b.PortName != "" {
		return b.PortName
	}
	return strconv.Itoa(b.Port)
}

func backendFromAnnotation(service, port string) Backend {
	if n, err := strconv.Atoi(port); err == nil {
		return Backend{Service: service, Port: n}
	}
	return Backend{Service: service, PortName: port}
}

// ErrNoServiceBackend is returned for Ingresses whose first rule has no Service backend.
var ErrNoServiceBackend = errors.New("ingress has no rule with a Service backend")

// managedIngressPath is the backend Smart Proxy manages: the first path of the first rule.
func managedIngressPath(ing *networkingv1.Ingress) *networkingv1.HTTPIngressPath {
	if len(ing.Spec.Rules) == 0 || ing.Spec.Rules[0].HTTP == nil || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
		return nil
	}
	path := &ing.Spec.Rules[0].HTTP.Paths[0]
	if path.Backend.Service == nil {
		return nil
	}
	return path
}

// IngressHost is the host of the Ingress's first rule.
func IngressHost(ing *networkingv1.Ingress) string {
	if len(ing.Spec.Rules) == 0 {
		return ""
	}
	return ing.Spec.Rules[0].Host
}

// IngressPath is the path of the managed backend ("/" when unset).
func IngressPath(ing *networkingv1.Ingress) string {
	if p := managedIngressPath(ing); p != nil && p.Path != "" {
		return p.Path
	}
	return "/"
}

// IngressBackend returns the backend the Ingress currently sends traffic to.
func IngressBackend(ing *networkingv1.Ingress) (Backend, bool) {
	path := managedIngressPath(ing)
	if path == nil {
		return Backend{}, false
	}
	svc := path.Backend.Service
	return Backend{Service: svc.Name, Port: int(svc.Port.Number), PortName: svc.Port.Name}, true
}

// OriginalIngressBackend returns the application backend: from the annotations when patched,
// otherwise the current one.
//
// When the spec no longer points at Smart Proxy's port (e.g. a Helm upgrade re-applied the
// application's Ingress, keeping the annotations), the spec is the truth: the recorded backend
// may be outdated.
func OriginalIngressBackend(ing *networkingv1.Ingress) (Backend, bool) {
	if ing.Annotations[AnnotationPatched] == "true" && ing.Annotations[AnnotationOriginalService] != "" && ingressPointsAtProxy(ing) {
		return backendFromAnnotation(ing.Annotations[AnnotationOriginalService], ing.Annotations[AnnotationOriginalPort]), true
	}
	return IngressBackend(ing)
}

// ingressPointsAtProxy reports whether the Ingress sends traffic to a Service's "proxy" port,
// as patched Ingresses do.
func ingressPointsAtProxy(ing *networkingv1.Ingress) bool {
	path := managedIngressPath(ing)
	return path != nil && path.Backend.Service != nil && path.Backend.Service.Port.Name == ProxyPortName
}

// routePointsAtProxy reports whether the Route targets a Service's "proxy" port, as patched
// Routes do.
func routePointsAtProxy(rt *routev1.Route) bool {
	return rt.Spec.Port != nil && rt.Spec.Port.TargetPort.StrVal == ProxyPortName
}

// IsIngressPatched reports whether the Ingress points at Smart Proxy's named port.
func IsIngressPatched(ing *networkingv1.Ingress, proxyService string) bool {
	path := managedIngressPath(ing)
	return ing.Annotations[AnnotationPatched] == "true" && path != nil &&
		path.Backend.Service.Name == proxyService && path.Backend.Service.Port.Name == ProxyPortName
}

// PatchIngress points the Ingress at Smart Proxy, recording the original backend and config.
func PatchIngress(ing *networkingv1.Ingress, proxyService string, original Backend, configJSON string) error {
	path := managedIngressPath(ing)
	if path == nil {
		return ErrNoServiceBackend
	}
	setAnnotations(&ing.Annotations, original, configJSON)
	path.Backend.Service.Name = proxyService
	path.Backend.Service.Port = networkingv1.ServiceBackendPort{Name: ProxyPortName}
	return nil
}

// UnpatchIngress restores the original backend and removes Smart Proxy's annotations.
func UnpatchIngress(ing *networkingv1.Ingress) error {
	if ing.Annotations[AnnotationPatched] != "true" {
		return errors.New("not patched")
	}
	original := backendFromAnnotation(ing.Annotations[AnnotationOriginalService], ing.Annotations[AnnotationOriginalPort])
	if original.Port == 0 && original.PortName == "" {
		original.Port = 80 // Legacy patches without a recorded port
	}
	if path := managedIngressPath(ing); path != nil {
		path.Backend.Service.Name = original.Service
		path.Backend.Service.Port = networkingv1.ServiceBackendPort{Number: int32(original.Port), Name: original.PortName}
	}
	clearAnnotations(ing.Annotations)
	return nil
}

// originalRouteBackends preserves what patching changes in a Route besides its Service: the
// traffic split and the exact port spec.
type originalRouteBackends struct {
	ToWeight          *int32                         `json:"toWeight,omitempty"`
	AlternateBackends []routev1.RouteTargetReference `json:"alternateBackends,omitempty"`
	// Port is the original spec.port (its targetPort refers to the endpoints: a container port
	// number or a port name, valid for every backend). PortRecorded tells a nil Port apart from
	// annotations written by older versions, which only kept the resolved Service port.
	Port         *routev1.RoutePort `json:"port,omitempty"`
	PortRecorded bool               `json:"portRecorded,omitempty"`
}

// RouteTarget is one of the Services an OpenShift Route sends traffic to.
type RouteTarget struct {
	Service string
	Weight  int32
}

// defaultRouteWeight is the weight OpenShift gives a backend without one.
const defaultRouteWeight = 100

func weightOf(w *int32) int32 {
	if w == nil {
		return defaultRouteWeight
	}
	return *w
}

// OriginalRouteTargets returns the Services the Route balances traffic across (its main
// Service first) and its port spec, as they were before patching.
func OriginalRouteTargets(rt *routev1.Route) ([]RouteTarget, *routev1.RoutePort) {
	to, toWeight, alternates, port := rt.Spec.To.Name, rt.Spec.To.Weight, rt.Spec.AlternateBackends, rt.Spec.Port
	if rt.Annotations[AnnotationPatched] == "true" && routePointsAtProxy(rt) {
		var orig originalRouteBackends
		_ = json.Unmarshal([]byte(rt.Annotations[AnnotationOriginalBackends]), &orig)
		to, toWeight, alternates = OriginalRouteService(rt), orig.ToWeight, orig.AlternateBackends
		port = orig.Port
		if !orig.PortRecorded {
			port = nil
		}
	}
	targets := []RouteTarget{{Service: to, Weight: weightOf(toWeight)}}
	for _, alt := range alternates {
		if alt.Kind == "" || alt.Kind == "Service" {
			targets = append(targets, RouteTarget{Service: alt.Name, Weight: weightOf(alt.Weight)})
		}
	}
	return targets, port
}

// RoutePath is the Route's path ("/" when unset).
func RoutePath(rt *routev1.Route) string {
	if rt.Spec.Path == "" {
		return "/"
	}
	return rt.Spec.Path
}

// OriginalRouteService returns the application Service: from the annotations when patched,
// otherwise (or when the spec was re-applied since, see OriginalIngressBackend) the current target.
func OriginalRouteService(rt *routev1.Route) string {
	if rt.Annotations[AnnotationPatched] == "true" && rt.Annotations[AnnotationOriginalService] != "" && routePointsAtProxy(rt) {
		return rt.Annotations[AnnotationOriginalService]
	}
	return rt.Spec.To.Name
}

// IsRoutePatched reports whether the Route points at Smart Proxy.
func IsRoutePatched(rt *routev1.Route, proxyService string) bool {
	return rt.Annotations[AnnotationPatched] == "true" && rt.Spec.To.Name == proxyService
}

// PatchRoute points the Route at Smart Proxy, recording the original backend, traffic split and config.
func PatchRoute(rt *routev1.Route, proxyService string, original Backend, configJSON string) {
	// Still pointing at us (re-patching after the annotations were lost or changed): the spec no
	// longer holds the original split and port, keep what was recorded.
	keepRecorded := IsProxyService(rt.Spec.To.Name, proxyService) && rt.Annotations[AnnotationOriginalBackends] != ""
	setAnnotations(&rt.Annotations, original, configJSON)
	if !keepRecorded {
		orig := originalRouteBackends{ToWeight: rt.Spec.To.Weight, AlternateBackends: rt.Spec.AlternateBackends, PortRecorded: true}
		if rt.Spec.Port != nil {
			orig.Port = rt.Spec.Port.DeepCopy()
		}
		if data, err := json.Marshal(orig); err == nil {
			rt.Annotations[AnnotationOriginalBackends] = string(data)
		}
	}
	rt.Spec.To.Name = proxyService
	rt.Spec.To.Weight = nil
	rt.Spec.AlternateBackends = nil
	if rt.Spec.Port == nil {
		rt.Spec.Port = &routev1.RoutePort{}
	}
	rt.Spec.Port.TargetPort = intstr.FromString(ProxyPortName)
}

// UnpatchRoute restores the original backend and traffic split and removes Smart Proxy's annotations.
func UnpatchRoute(rt *routev1.Route) error {
	if rt.Annotations[AnnotationPatched] != "true" {
		return errors.New("not patched")
	}
	rt.Spec.To.Name = rt.Annotations[AnnotationOriginalService]

	var backends originalRouteBackends
	if data := rt.Annotations[AnnotationOriginalBackends]; data != "" {
		_ = json.Unmarshal([]byte(data), &backends)
	}

	if backends.PortRecorded {
		rt.Spec.Port = backends.Port
	} else {
		// Patched by an older version: only the resolved port was kept.
		port := rt.Annotations[AnnotationOriginalPort]
		switch n, err := strconv.Atoi(port); {
		case port == "":
			rt.Spec.Port = nil
		case err == nil:
			rt.Spec.Port = &routev1.RoutePort{TargetPort: intstr.FromInt(n)}
		default:
			rt.Spec.Port = &routev1.RoutePort{TargetPort: intstr.FromString(port)}
		}
	}
	rt.Spec.To.Weight = backends.ToWeight
	rt.Spec.AlternateBackends = backends.AlternateBackends

	clearAnnotations(rt.Annotations)
	return nil
}

func setAnnotations(annotations *map[string]string, original Backend, configJSON string) {
	if *annotations == nil {
		*annotations = map[string]string{}
	}
	a := *annotations
	a[AnnotationPatched] = "true"
	a[AnnotationOriginalService] = original.Service
	a[AnnotationOriginalPort] = original.PortString()
	if configJSON != "" {
		a[AnnotationConfig] = configJSON
	}
}

func clearAnnotations(annotations map[string]string) {
	for _, key := range []string{AnnotationPatched, AnnotationOriginalService, AnnotationOriginalPort, AnnotationOriginalBackends, AnnotationConfig, AnnotationDeclarative} {
		delete(annotations, key)
	}
}
