package watcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"

	routev1 "github.com/openshift/api/route/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type Watcher struct {
	k8sClient   *k8s.Client
	store       *store.Store
	serviceName string          // Service fronting Smart Proxy; patched Ingresses/Routes point at it
	kedaWarned  map[string]bool // Deployments already reported as KEDA-managed (only touched by the watcher loop)
}

func NewWatcher(k8sClient *k8s.Client, store *store.Store, serviceName string) *Watcher {
	return &Watcher{
		k8sClient:   k8sClient,
		store:       store,
		serviceName: serviceName,
		kedaWarned:  make(map[string]bool),
	}
}

func (w *Watcher) Start() {
	logger.Println("Watcher started. Checking for idle services every 30s...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		w.checkIdleRoutes()
		w.healUnpatchedRoutes()
	}
}

func (w *Watcher) checkIdleRoutes() {
	routes := w.store.GetAllRoutes()

	for _, route := range routes {
		// SAFETY: Only scale down if the route config represents a patched resource (Ingress or Route).
		// Manual configs (UUIDs) do not have traffic routed through smart-proxy, so scaling them down
		// would make them unreachable permanently.
		if !strings.HasPrefix(route.ID, "route-") && !strings.HasPrefix(route.ID, "ing-") {
			continue
		}
		if time.Since(route.LastActivity) > idleTimeout(route) {
			// 1. Main deployment
			// Always On deployments and those still needed by another active route stay up.
			// (Not logged: this runs every tick for every idle route and would flood the log view.)
			if !route.AlwaysOn && !w.isDeploymentActive(routes, route.Namespace, route.Deployment) {
				w.sleep(route.Namespace, route.Deployment, fmt.Sprintf("route %s idle since %s", route.Path, route.LastActivity.Format(time.RFC3339)))
			}

			// 2. Scale down dependencies if they are not active in any other route
			for _, dep := range route.Dependencies {
				if dep.StopOnIdle && !w.isDeploymentActive(routes, route.Namespace, dep.Name) {
					w.sleep(route.Namespace, dep.Name, fmt.Sprintf("dependency of idle route %s", route.Path))
				}
			}
		}
	}
}

// defaultIdleTimeout applies to routes saved without one; a zero timeout would put the
// deployment back to sleep on every watcher tick.
const defaultIdleTimeout = 30 * time.Minute

func idleTimeout(r store.RouteConfig) time.Duration {
	if r.IdleTimeout <= 0 {
		return defaultIdleTimeout
	}
	return r.IdleTimeout
}

// sleep scales a deployment to zero if it is running, logging only when something happens.
func (w *Watcher) sleep(namespace, deployment, reason string) {
	key := namespace + "/" + deployment
	slept, err := w.k8sClient.SleepDeployment(namespace, deployment)
	switch {
	case errors.Is(err, k8s.ErrManagedByKEDA):
		if !w.kedaWarned[key] {
			w.kedaWarned[key] = true
			logger.Printf("Not putting %s to sleep: its %v, which would scale it back up. Use KEDA's own scale-to-zero, or remove the ScaledObject.", key, err)
		}
	case err != nil:
		logger.Printf("Error putting %s to sleep: %v", key, err)
	case slept:
		logger.Printf("Scaled down %s (%s)", key, reason)
	}
}

// isDeploymentActive checks if a deployment is needed by any active route (either as main deployment or dependency)
func (w *Watcher) isDeploymentActive(routes []store.RouteConfig, namespace, deploymentName string) bool {
	for _, r := range routes {
		if r.Namespace != namespace {
			continue
		}
		// If the route itself is active (last request is within its idle timeout)
		if time.Since(r.LastActivity) <= idleTimeout(r) {
			// Check if it's the main deployment
			if r.Deployment == deploymentName {
				return true
			}
			// Check if it's in the dependencies
			for _, dep := range r.Dependencies {
				if dep.Name == deploymentName {
					return true
				}
			}
		}
	}
	return false
}

// legacyServiceName is the Service name older versions always patched resources to,
// regardless of the actual Service name.
const legacyServiceName = "smart-proxy"

// isProxyService reports whether a backend Service name points at Smart Proxy itself,
// including resources patched by older versions under the legacy name.
func (w *Watcher) isProxyService(name string) bool {
	return name == w.serviceName || name == legacyServiceName
}

func (w *Watcher) healUnpatchedRoutes() {
	if w.k8sClient == nil {
		return
	}

	routes, err := w.k8sClient.ListRoutes()
	if err != nil {
		logger.Printf("Self-Healing Warning: Failed to list OpenShift routes: %v", err)
		return
	}

	configs := w.store.GetAllRoutes()
	for _, route := range configs {
		if strings.HasPrefix(route.ID, "route-") {
			primaryRouteName := route.ID[6:]
			configHosts := strings.Split(route.Host, ",")
			for i := range configHosts {
				configHosts[i] = strings.TrimSpace(configHosts[i])
			}

			for _, rt := range routes {
				isPrimary := rt.Name == primaryRouteName
				hostMatch := false
				for _, h := range configHosts {
					if strings.EqualFold(h, rt.Spec.Host) {
						hostMatch = true
						break
					}
				}

				if isPrimary || hostMatch {
					// Check if it is patched (spec targets smart-proxy and annotation is present)
					isPatched := rt.Annotations["smart-proxy/patched"] == "true" && rt.Spec.To.Name == w.serviceName
					if !isPatched {
						logger.Printf("Self-Healing: Route %s has been unpatched (likely by Helm). Re-applying patch...", rt.Name)

						originalSvc := rt.Spec.To.Name
						targetPort := route.TargetPort
						if w.isProxyService(originalSvc) {
							// Still pointing at us, so the spec no longer holds the original target.
							originalSvc = route.TargetService
						} else if p, err := w.k8sClient.ResolveServicePort(originalSvc, rt.Spec.Port); err == nil {
							targetPort = p
						}

						if rt.Annotations == nil {
							rt.Annotations = make(map[string]string)
						}
						rt.Annotations["smart-proxy/patched"] = "true"
						rt.Annotations["smart-proxy/original-service"] = originalSvc
						rt.Annotations["smart-proxy/original-port"] = strconv.Itoa(targetPort)

						// Save original to weight and alternate backends
						type originalBackends struct {
							ToWeight          *int32                         `json:"toWeight,omitempty"`
							AlternateBackends []routev1.RouteTargetReference `json:"alternateBackends,omitempty"`
						}
						origBackends := originalBackends{
							ToWeight:          rt.Spec.To.Weight,
							AlternateBackends: rt.Spec.AlternateBackends,
						}
						if origBackendsBytes, err := json.Marshal(origBackends); err == nil {
							rt.Annotations["smart-proxy/original-backends"] = string(origBackendsBytes)
						}

						rt.Spec.To.Name = w.serviceName
						rt.Spec.To.Weight = nil
						rt.Spec.AlternateBackends = nil

						if rt.Spec.Port == nil {
							rt.Spec.Port = &routev1.RoutePort{}
						}
						rt.Spec.Port.TargetPort = intstr.FromString(k8s.ProxyPortName)

						configBytes, _ := json.Marshal(route)
						rt.Annotations["smart-proxy/config"] = string(configBytes)

						if err := w.k8sClient.UpdateRoute(rt); err != nil {
							logger.Printf("Self-Healing Warning: Failed to re-apply patch to route %s: %v", rt.Name, err)
						} else {
							logger.Printf("Self-Healing Success: Re-applied patch to route %s", rt.Name)
						}
					}
				}
			}
		} else if strings.HasPrefix(route.ID, "ing-") {
			ingressName := route.ID[4:]
			ing, err := w.k8sClient.GetIngress(ingressName)
			if err != nil {
				continue
			}

			if len(ing.Spec.Rules) == 0 || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
				continue
			}
			path := ing.Spec.Rules[0].HTTP.Paths[0]

			// Also require the named port: older versions pointed at a port number that could
			// mismatch the Service, and re-patching migrates those Ingresses.
			isPatched := ing.Annotations["smart-proxy/patched"] == "true" &&
				path.Backend.Service.Name == w.serviceName &&
				path.Backend.Service.Port.Name == k8s.ProxyPortName
			if !isPatched {
				logger.Printf("Self-Healing: Ingress %s has been unpatched. Re-applying patch...", ingressName)

				originalSvc := path.Backend.Service.Name
				originalPort := int(path.Backend.Service.Port.Number)
				if w.isProxyService(originalSvc) {
					// Still pointing at us, so the backend no longer holds the original target.
					originalSvc = route.TargetService
					originalPort = route.TargetPort
				}

				if ing.Annotations == nil {
					ing.Annotations = make(map[string]string)
				}
				ing.Annotations["smart-proxy/patched"] = "true"
				ing.Annotations["smart-proxy/original-service"] = originalSvc
				ing.Annotations["smart-proxy/original-port"] = strconv.Itoa(originalPort)

				path.Backend.Service.Name = w.serviceName
				path.Backend.Service.Port = networkingv1.ServiceBackendPort{Name: k8s.ProxyPortName}
				ing.Spec.Rules[0].HTTP.Paths[0] = path

				configBytes, _ := json.Marshal(route)
				ing.Annotations["smart-proxy/config"] = string(configBytes)

				if err := w.k8sClient.UpdateIngress(ing); err != nil {
					logger.Printf("Self-Healing Warning: Failed to re-apply patch to ingress %s: %v", ingressName, err)
				} else {
					logger.Printf("Self-Healing Success: Re-applied patch to ingress %s", ingressName)
				}
			}
		}
	}
}
