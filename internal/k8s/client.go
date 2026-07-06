// Package k8s provides a client for interacting with Kubernetes and OpenShift clusters.
// It abstracts common operations like scaling deployments, listing ingresses, and managing OpenShift routes.
package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sync"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	routev1 "github.com/openshift/api/route/v1"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	routev1client "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"
)

// Client wraps the Kubernetes and OpenShift clientsets.
type Client struct {
	Clientset      *kubernetes.Clientset
	RouteClientSet *routeclientset.Clientset
	RouteClient    routev1client.RouteV1Interface // Interface for interacting with OpenShift Routes
	Namespace      string                         // The namespace the client is scoped to
	probeCache     map[string][]string            // Cache of probe paths per deployment (namespace/name -> paths)
	probeCacheMu   sync.RWMutex
}

// NewClient creates a new instance of the K8s Client.
// It attempts to load configuration from the cluster environment or a local kubeconfig file.
// It automatically detects the current namespace if running in a cluster, or falls back to "default".
func NewClient() (*Client, error) {
	var config *rest.Config
	var err error

	// Check if running inside cluster
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err = rest.InClusterConfig()
	} else {
		// Use kubeconfig from home directory
		var kubeconfig string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		} else {
			return nil, fmt.Errorf("home directory not found")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	if err != nil {
		return nil, err
	}

	// Optimize client settings to avoid client-side throttling (default is 5 QPS / 10 Burst)
	config.QPS = 100
	config.Burst = 150

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// Determine namespace
	// 1. Env var "WATCH_NAMESPACE"
	// 2. Fallback to "default" (or read from service account mount in future)
	ns := os.Getenv("WATCH_NAMESPACE")
	if ns == "" {
		// Try to read from service account secret if running in cluster
		if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			ns = string(data)
		} else {
			ns = "default"
		}
	}

	// Initialize OpenShift Route Client
	routeClient, err := routeclientset.NewForConfig(config)
	if err != nil {
		// Log warning but don't fail, maybe not on OpenShift
		fmt.Printf("Warning: Failed to create OpenShift Route client: %v\n", err)
	}

	if routeClient != nil {
		_ = routeClient.RouteV1().Routes(ns) // Just to verify we can get the interface
	}

	return &Client{
		Clientset:      clientset,
		RouteClient:    routeClient.RouteV1(), // Store the V1 interface to create namespaced clients on fly or just store clientset
		RouteClientSet: routeClient,
		Namespace:      ns,
		probeCache:     make(map[string][]string),
	}, nil
}

// GetDeploymentStatus checks if a deployment is ready (replicas > 0 and available)
// GetDeploymentStatus returns the number of replicas and ready replicas for a deployment.
// If the namespace is empty, it uses the client's scoped namespace.
func (c *Client) GetDeploymentStatus(namespace, deploymentName string) (int32, int32, error) {
	// If namespace is not provided or different (should not happen in single-ns mode logic), enforce strictness or allow if empty
	targetNs := namespace
	if targetNs == "" {
		targetNs = c.Namespace
	}

	deployment, err := c.Clientset.AppsV1().Deployments(targetNs).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}
	return *deployment.Spec.Replicas, deployment.Status.ReadyReplicas, nil
}

// ScaleDeployment scales a deployment to a specific number of replicas
func (c *Client) ScaleDeployment(namespace, deploymentName string, replicas int32) error {
	targetNs := namespace
	if targetNs == "" {
		targetNs = c.Namespace
	}

	scale, err := c.Clientset.AppsV1().Deployments(targetNs).GetScale(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	sc := *scale
	sc.Spec.Replicas = replicas

	_, err = c.Clientset.AppsV1().Deployments(targetNs).UpdateScale(context.TODO(), deploymentName, &sc, metav1.UpdateOptions{})
	return err
}

// ListNamespaces returns ONLY the current namespace in single-ns mode
func (c *Client) ListNamespaces() ([]string, error) {
	return []string{c.Namespace}, nil
}

// ListDeployments lists deployments in the scoped namespace
func (c *Client) ListDeployments(namespace string) ([]string, error) {
	// Ignore the passed namespace argument if we want to enforce single-ns,
	// or use it if we trust the caller. For safety/transparency in single-ns mode, use c.Namespace
	targetNs := c.Namespace // Enforce scoped namespace

	deployments, err := c.Clientset.AppsV1().Deployments(targetNs).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range deployments.Items {
		names = append(names, d.Name)
	}
	return names, nil
}

// ListIngresses lists all ingresses in the namespace
func (c *Client) ListIngresses() ([]*networkingv1.Ingress, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("k8s client not initialized")
	}
	list, err := c.Clientset.NetworkingV1().Ingresses(c.Namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var result []*networkingv1.Ingress
	for i := range list.Items {
		result = append(result, &list.Items[i])
	}
	return result, nil
}

// GetIngress gets a specific ingress
func (c *Client) GetIngress(name string) (*networkingv1.Ingress, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("k8s client not initialized")
	}
	return c.Clientset.NetworkingV1().Ingresses(c.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
}

// UpdateIngress updates an existing ingress
func (c *Client) UpdateIngress(ingress *networkingv1.Ingress) error {
	if c.Clientset == nil {
		return fmt.Errorf("k8s client not initialized")
	}
	_, err := c.Clientset.NetworkingV1().Ingresses(c.Namespace).Update(context.TODO(), ingress, metav1.UpdateOptions{})
	return err
}

// OpenShift Route Support

// ListRoutes lists all routes in the namespace
func (c *Client) ListRoutes() ([]*routev1.Route, error) {
	if c.RouteClient == nil {
		return nil, fmt.Errorf("route client not initialized")
	}
	list, err := c.RouteClient.Routes(c.Namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var result []*routev1.Route
	for i := range list.Items {
		result = append(result, &list.Items[i])
	}
	return result, nil
}

// GetRoute gets a specific route
func (c *Client) GetRoute(name string) (*routev1.Route, error) {
	if c.RouteClient == nil {
		return nil, fmt.Errorf("route client not initialized")
	}
	return c.RouteClient.Routes(c.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
}

// UpdateRoute updates an existing route
func (c *Client) UpdateRoute(route *routev1.Route) error {
	if c.RouteClient == nil {
		return fmt.Errorf("route client not initialized")
	}
	_, err := c.RouteClient.Routes(c.Namespace).Update(context.TODO(), route, metav1.UpdateOptions{})
	return err
}

// ResolveDeploymentForService attempts to find the Deployment name corresponding to a Service.
func (c *Client) ResolveDeploymentForService(serviceName string) (string, error) {
	// 1. Try stripping "-svc" suffix if present
	if strings.HasSuffix(serviceName, "-svc") {
		depName := strings.TrimSuffix(serviceName, "-svc")
		_, err := c.Clientset.AppsV1().Deployments(c.Namespace).Get(context.TODO(), depName, metav1.GetOptions{})
		if err == nil {
			return depName, nil
		}
	}

	// 2. Try exact match (Service Name == Deployment Name)
	_, err := c.Clientset.AppsV1().Deployments(c.Namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil {
		return serviceName, nil
	}

	// 3. Query the Service selector and find a matching Deployment
	svc, err := c.Clientset.CoreV1().Services(c.Namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil && len(svc.Spec.Selector) > 0 {
		deps, err := c.Clientset.AppsV1().Deployments(c.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err == nil {
			for _, dep := range deps.Items {
				// Check if deployment's template selector matches the service selector
				match := true
				for k, v := range svc.Spec.Selector {
					if depVal, ok := dep.Spec.Selector.MatchLabels[k]; !ok || depVal != v {
						match = false
						break
					}
				}
				if match {
					return dep.Name, nil
				}
			}
		}
	}

	// Fallback to the service name
	return serviceName, nil
}

// ResolveServicePort finds the port number for a given service and targetPort spec
func (c *Client) ResolveServicePort(serviceName string, routePort *routev1.RoutePort) (int, error) {
	svc, err := c.Clientset.CoreV1().Services(c.Namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}

	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", serviceName)
	}

	// If no port specified in the route, default to the first service port
	if routePort == nil || routePort.TargetPort.String() == "" {
		return int(svc.Spec.Ports[0].Port), nil
	}

	tp := routePort.TargetPort
	if tp.Type == intstr.Int {
		return int(tp.IntVal), nil
	}

	// Named port: find matching port in Service
	portName := tp.StrVal
	for _, p := range svc.Spec.Ports {
		if p.Name == portName {
			return int(p.Port), nil
		}
	}

	// Fallback to first port if name not found
	return int(svc.Spec.Ports[0].Port), nil
}

// ResolveServiceForDeployment finds the Service and Port for a given Deployment
func (c *Client) ResolveServiceForDeployment(deploymentName string) (string, int, error) {
	// 1. Try checking if there's a Service named deploymentName + "-svc"
	svcName := deploymentName + "-svc"
	svc, err := c.Clientset.CoreV1().Services(c.Namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
	if err == nil && len(svc.Spec.Ports) > 0 {
		return svcName, int(svc.Spec.Ports[0].Port), nil
	}

	// 2. Try checking if there's a Service with the exact deploymentName
	svc, err = c.Clientset.CoreV1().Services(c.Namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err == nil && len(svc.Spec.Ports) > 0 {
		return deploymentName, int(svc.Spec.Ports[0].Port), nil
	}

	// 3. Find by matching Deployment labels and Service selector
	dep, err := c.Clientset.AppsV1().Deployments(c.Namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err == nil {
		svcs, err := c.Clientset.CoreV1().Services(c.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err == nil {
			for _, s := range svcs.Items {
				if len(s.Spec.Selector) > 0 {
					match := true
					for k, v := range s.Spec.Selector {
						if depVal, ok := dep.Spec.Template.Labels[k]; !ok || depVal != v {
							match = false
							break
						}
					}
					if match && len(s.Spec.Ports) > 0 {
						return s.Name, int(s.Spec.Ports[0].Port), nil
					}
				}
			}
		}
	}

	return "", 0, fmt.Errorf("could not find service for deployment %s", deploymentName)
}

// GetDeploymentProbePaths returns all HTTP probe paths (readiness, liveness, startup) defined for the deployment, using an in-memory cache.
func (c *Client) GetDeploymentProbePaths(namespace, deploymentName string) ([]string, error) {
	targetNs := namespace
	if targetNs == "" {
		targetNs = c.Namespace
	}
	key := targetNs + "/" + deploymentName

	c.probeCacheMu.RLock()
	paths, exists := c.probeCache[key]
	c.probeCacheMu.RUnlock()
	if exists {
		return paths, nil
	}

	deployment, err := c.Clientset.AppsV1().Deployments(targetNs).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	var newPaths []string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.ReadinessProbe != nil && container.ReadinessProbe.HTTPGet != nil {
			path := container.ReadinessProbe.HTTPGet.Path
			if path != "" {
				newPaths = append(newPaths, path)
			}
		}
		if container.LivenessProbe != nil && container.LivenessProbe.HTTPGet != nil {
			path := container.LivenessProbe.HTTPGet.Path
			if path != "" {
				newPaths = append(newPaths, path)
			}
		}
		if container.StartupProbe != nil && container.StartupProbe.HTTPGet != nil {
			path := container.StartupProbe.HTTPGet.Path
			if path != "" {
				newPaths = append(newPaths, path)
			}
		}
	}

	// Remove duplicates from newPaths
	uniquePaths := make([]string, 0, len(newPaths))
	seen := make(map[string]bool)
	for _, p := range newPaths {
		if !seen[p] {
			seen[p] = true
			uniquePaths = append(uniquePaths, p)
		}
	}

	c.probeCacheMu.Lock()
	if c.probeCache == nil {
		c.probeCache = make(map[string][]string)
	}
	c.probeCache[key] = uniquePaths
	c.probeCacheMu.Unlock()

	return uniquePaths, nil
}

