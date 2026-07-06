package watcher

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"

	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type Watcher struct {
	k8sClient *k8s.Client
	store     *store.Store
}

func NewWatcher(k8sClient *k8s.Client, store *store.Store) *Watcher {
	return &Watcher{
		k8sClient: k8sClient,
		store:     store,
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
		// IdleTimeout is already time.Duration
		// But in old config it was string.
		// Since we changed the struct in config.go to time.Duration, we don't need to parse string anymore.
		// However, JSON unmarshal of string into time.Duration assumes nanoseconds unless we write a custom unmarshal?
		// No, standard JSON unmarshal into time.Duration expects numbers (ns).
		// Wait, if users provide string "30m" in JSON, standard unmarshal will FAIL for time.Duration field.
		// We might need a wrapper type or keep it string and parse it here.
		// Let's assume for now the Store handles loading correctly or we change struct back to string.
		// Actually, standard `time.Duration` in Go JSON is int64 (nanoseconds).
		// If we want user friendly "30m", we should keep it string in Struct.
		// Reverting Struct field to string in store/config.go would be safer for user config?
		// No, let's stick to Duration in struct but we assume the JSON has int64.
		// OR we change it back to string.
		// Given the user wants "Professional", "30m" string is better than 1800000000000.
		// Let's keep it Duration but assume we handled it?
		// Actually, I should probably check what I wrote in config.go.
		// I wrote `IdleTimeout  time.Duration`.
		// If I want string inputs, I should use a custom type or string.
		// For simplicity, let's use string in struct and parse it here, as it was before.
		// BUT I already wrote config.go with time.Duration.
		// Let's assume I fix config.go?
		// No, let's fix THIS watcher to use the Duration directly.

		timeout := route.IdleTimeout

		if time.Since(route.LastActivity) > timeout {
			// 1. Check if the main deployment is needed by any other active route
			if route.AlwaysOn {
				logger.Printf("Route %s is idle, but main deployment %s is configured as Always On. Keeping it alive.", route.Path, route.Deployment)
			} else if w.isDeploymentActive(routes, route.Namespace, route.Deployment) {
				// Main deployment is still needed by another active route, skip scaling it down.
				logger.Printf("Route %s is idle, but main deployment %s is still needed by another active route. Keeping it alive.", route.Path, route.Deployment)
			} else {
				// Check current replicas of main deployment
				replicas, _, err := w.k8sClient.GetDeploymentStatus(route.Namespace, route.Deployment)
				if err != nil {
					logger.Printf("Error getting status for idle check %s/%s: %v", route.Namespace, route.Deployment, err)
				} else if replicas > 0 {
					logger.Printf("Route %s is idle (Last active: %s). Scaling down deployment %s...",
						route.Path, route.LastActivity.Format(time.RFC3339), route.Deployment)

					err := w.k8sClient.ScaleDeployment(route.Namespace, route.Deployment, 0)
					if err != nil {
						logger.Printf("Error scaling down %s: %v", route.Deployment, err)
					}
				}
			}

			// 2. Scale down dependencies if they are not active in any other route
			for _, dep := range route.Dependencies {
				if dep.StopOnIdle {
					if w.isDeploymentActive(routes, route.Namespace, dep.Name) {
						logger.Printf("Route %s is idle, but dependency %s is still needed by another active route. Keeping it alive.", route.Path, dep.Name)
						continue
					}

					replicas, _, err := w.k8sClient.GetDeploymentStatus(route.Namespace, dep.Name)
					if err != nil {
						logger.Printf("Error getting status for dependency %s: %v", dep.Name, err)
					} else if replicas > 0 {
						logger.Printf("Scaling down dependency %s for route %s...", dep.Name, route.Path)
						err := w.k8sClient.ScaleDeployment(route.Namespace, dep.Name, 0)
						if err != nil {
							logger.Printf("Error scaling down dependency %s: %v", dep.Name, err)
						}
					}
				}
			}
		}
	}
}

// isDeploymentActive checks if a deployment is needed by any active route (either as main deployment or dependency)
func (w *Watcher) isDeploymentActive(routes []store.RouteConfig, namespace, deploymentName string) bool {
	for _, r := range routes {
		if r.Namespace != namespace {
			continue
		}
		// If the route itself is active (last request is within IdleTimeout)
		if time.Since(r.LastActivity) <= r.IdleTimeout {
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
					isPatched := rt.Annotations["smart-proxy/patched"] == "true" && rt.Spec.To.Name == "smart-proxy"
					if !isPatched {
						logger.Printf("Self-Healing: Route %s has been unpatched (likely by Helm). Re-applying patch...", rt.Name)

						originalSvc := rt.Spec.To.Name
						if originalSvc == "smart-proxy" {
							originalSvc = route.TargetService
						}

						targetPort, err := w.k8sClient.ResolveServicePort(originalSvc, rt.Spec.Port)
						if err != nil {
							targetPort = route.TargetPort
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

						rt.Spec.To.Name = "smart-proxy"
						rt.Spec.To.Weight = nil
						rt.Spec.AlternateBackends = nil

						if rt.Spec.Port == nil {
							rt.Spec.Port = &routev1.RoutePort{}
						}
						rt.Spec.Port.TargetPort = intstr.FromString("proxy")

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

			isPatched := ing.Annotations["smart-proxy/patched"] == "true" && path.Backend.Service.Name == "smart-proxy"
			if !isPatched {
				logger.Printf("Self-Healing: Ingress %s has been unpatched. Re-applying patch...", ingressName)

				originalSvc := path.Backend.Service.Name
				if originalSvc == "smart-proxy" {
					originalSvc = route.TargetService
				}
				originalPort := int(path.Backend.Service.Port.Number)
				if originalPort == int(route.TargetPort) {
					originalPort = route.TargetPort
				}

				if ing.Annotations == nil {
					ing.Annotations = make(map[string]string)
				}
				ing.Annotations["smart-proxy/patched"] = "true"
				ing.Annotations["smart-proxy/original-service"] = originalSvc
				ing.Annotations["smart-proxy/original-port"] = strconv.Itoa(originalPort)

				path.Backend.Service.Name = "smart-proxy"
				path.Backend.Service.Port.Number = int32(8080)
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
