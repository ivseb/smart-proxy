// Package k8s provides a client for interacting with Kubernetes and OpenShift clusters.
// Reads are served from informer caches (no API call per proxied request); writes go to the API.
// The client can watch one namespace, a list of namespaces, or every namespace (optionally
// filtered by a namespace label selector).
package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	appsclientset "github.com/openshift/client-go/apps/clientset/versioned"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
)

// ProxyPortName is the name of the Smart Proxy Service port that receives proxied traffic.
// Patched Ingresses and Routes reference it by name, so they don't depend on the port number.
const ProxyPortName = "proxy"

// Scope says which namespaces Smart Proxy manages.
type Scope struct {
	// All watches every namespace (filtered by Selector when set).
	All bool
	// Namespaces is the explicit list watched when All is false.
	Namespaces []string
	// Selector restricts All to namespaces whose labels match.
	Selector labels.Selector
}

// ParseScope reads WATCH_NAMESPACE ("" = own namespace, "a,b" = a list, "*" = all) and
// WATCH_NAMESPACE_SELECTOR (a label selector on namespaces; implies all namespaces).
func ParseScope(watch, selector, ownNamespace string) (Scope, error) {
	watch = strings.TrimSpace(watch)
	selector = strings.TrimSpace(selector)

	if selector != "" {
		if watch != "" && watch != "*" {
			return Scope{}, fmt.Errorf("WATCH_NAMESPACE_SELECTOR cannot be combined with an explicit WATCH_NAMESPACE list")
		}
		sel, err := labels.Parse(selector)
		if err != nil {
			return Scope{}, fmt.Errorf("invalid WATCH_NAMESPACE_SELECTOR %q: %w", selector, err)
		}
		return Scope{All: true, Selector: sel}, nil
	}
	if watch == "*" {
		return Scope{All: true}, nil
	}

	seen := map[string]bool{}
	var namespaces []string
	for _, ns := range strings.Split(watch, ",") {
		if ns = strings.TrimSpace(ns); ns != "" && !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	if len(namespaces) == 0 {
		namespaces = []string{ownNamespace}
	}
	sort.Strings(namespaces)
	return Scope{Namespaces: namespaces}, nil
}

func (s Scope) String() string {
	switch {
	case s.All && s.Selector != nil:
		return fmt.Sprintf("namespaces matching %q", s.Selector.String())
	case s.All:
		return "all namespaces"
	default:
		return "namespaces " + strings.Join(s.Namespaces, ", ")
	}
}

// Client wraps the Kubernetes and OpenShift clientsets and their informer caches.
type Client struct {
	Clientset      kubernetes.Interface
	RouteClientSet routeclientset.Interface
	AppsClientSet  appsclientset.Interface // OpenShift DeploymentConfigs

	scope        Scope
	ownNamespace string
	cache        *informerCache

	hpaWarnOnce sync.Once

	wakeMu  sync.Mutex
	waking  map[string]*wakeCall // Wake-ups in progress, shared by concurrent requests
	wokenAt map[string]time.Time // Recent wake-ups, so a burst of requests doesn't repeat them
}

type wakeCall struct {
	done chan struct{}
	err  error
}

// NewClient connects to the cluster (in-cluster config, or ~/.kube/config outside a cluster).
// Call Start before using it.
func NewClient(scope Scope, ownNamespace string) (*Client, error) {
	config, err := restConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	routeClient, err := routeclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating OpenShift Route client: %w", err)
	}
	appsClient, err := appsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating OpenShift apps client: %w", err)
	}
	return newClient(clientset, routeClient, appsClient, scope, ownNamespace), nil
}

// NewWithClients builds a Client on existing clientsets (e.g. fakes in tests).
func NewWithClients(clientset kubernetes.Interface, routeClient routeclientset.Interface, appsClient appsclientset.Interface, scope Scope, ownNamespace string) *Client {
	return newClient(clientset, routeClient, appsClient, scope, ownNamespace)
}

func newClient(clientset kubernetes.Interface, routeClient routeclientset.Interface, appsClient appsclientset.Interface, scope Scope, ownNamespace string) *Client {
	return &Client{
		Clientset:      clientset,
		RouteClientSet: routeClient,
		AppsClientSet:  appsClient,
		scope:          scope,
		ownNamespace:   ownNamespace,
	}
}

func restConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err = rest.InClusterConfig()
	} else {
		home := homedir.HomeDir()
		if home == "" {
			return nil, fmt.Errorf("home directory not found")
		}
		config, err = clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
	}
	if err != nil {
		return nil, err
	}
	// Reads come from caches, so API traffic is only writes and the initial lists.
	config.QPS = 50
	config.Burst = 100
	return config, nil
}

// OwnNamespace returns the namespace Smart Proxy runs in: POD_NAMESPACE, the service account
// namespace when in a cluster, or "default".
func OwnNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "default"
}

// Scope returns the namespaces this client manages.
func (c *Client) Scope() Scope {
	return c.scope
}

// DefaultNamespace is used when an API caller doesn't say which namespace it means
// (older dashboards and scripts): Smart Proxy's own namespace if watched, else the first watched one.
func (c *Client) DefaultNamespace() string {
	if c.Watches(c.ownNamespace) {
		return c.ownNamespace
	}
	if ns := c.WatchedNamespaces(); len(ns) > 0 {
		return ns[0]
	}
	return c.ownNamespace
}
