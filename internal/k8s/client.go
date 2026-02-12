// Package k8s provides a client for interacting with Kubernetes and OpenShift clusters.
// It abstracts common operations like scaling deployments, listing ingresses, and managing OpenShift routes.
package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		Clientset:   clientset,
		RouteClient: routeClient.RouteV1(), // Store the V1 interface to create namespaced clients on fly or just store clientset
		// Actually better to store the Interface for the namespace if scoped, or Clientset.
		// Let's store Clientset or typed interface.
		// To match existing pattern, let's store the clientset wrapper or similar.
		// Simplified:
		RouteClientSet: routeClient,
		Namespace:      ns,
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
