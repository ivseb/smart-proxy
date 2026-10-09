package k8s

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	appslisters "k8s.io/client-go/listers/apps/v1"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
	corelisters "k8s.io/client-go/listers/core/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	toolscache "k8s.io/client-go/tools/cache"

	routeinformers "github.com/openshift/client-go/route/informers/externalversions"
	routelisters "github.com/openshift/client-go/route/listers/route/v1"

	"smart-proxy/internal/logger"
)

// informerCache holds one informer factory per watched namespace, or a single cluster-wide
// factory (key "") when watching all namespaces.
type informerCache struct {
	factories      map[string]informers.SharedInformerFactory
	routeFactories map[string]routeinformers.SharedInformerFactory
	namespaces     corelisters.NamespaceLister // set when watching all namespaces

	hpaEnabled    bool
	routesEnabled bool
}

// Start builds the informer caches and waits until they are filled. Reads must not happen before.
func (c *Client) Start(ctx context.Context, timeout time.Duration) error {
	keys := c.scope.Namespaces
	if c.scope.All {
		keys = []string{metav1.NamespaceAll}
	}

	ic := &informerCache{
		factories:      map[string]informers.SharedInformerFactory{},
		routeFactories: map[string]routeinformers.SharedInformerFactory{},
		routesEnabled:  c.routesServed(),
		hpaEnabled:     true,
	}
	if !ic.routesEnabled {
		logger.Println("OpenShift Routes API not found; managing Ingresses only")
	}

	var synced []toolscache.InformerSynced
	for _, ns := range keys {
		// HPAs and Routes are optional: only watch them when RBAC allows, so a missing
		// permission degrades features instead of blocking startup.
		if ic.hpaEnabled && !c.canList(ctx, "autoscaling", "horizontalpodautoscalers", ns) {
			ic.hpaEnabled = false
			logger.Printf("Warning: no permission to list HorizontalPodAutoscalers in %s; HPA/KEDA-aware scaling is disabled", nsLabel(ns))
		}
		if ic.routesEnabled && !c.canList(ctx, "route.openshift.io", "routes", ns) {
			ic.routesEnabled = false
			logger.Printf("Warning: no permission to list OpenShift Routes in %s; managing Ingresses only", nsLabel(ns))
		}
	}

	for _, ns := range keys {
		f := informers.NewSharedInformerFactoryWithOptions(c.Clientset, 0, informers.WithNamespace(ns))
		ic.factories[ns] = f
		synced = append(synced,
			register(f.Apps().V1().Deployments().Informer()),
			register(f.Core().V1().Services().Informer()),
			register(f.Networking().V1().Ingresses().Informer()),
		)
		if ic.hpaEnabled {
			synced = append(synced, register(f.Autoscaling().V2().HorizontalPodAutoscalers().Informer()))
		}
		if ic.routesEnabled {
			rf := routeinformers.NewSharedInformerFactoryWithOptions(c.RouteClientSet, 0, routeinformers.WithNamespace(ns))
			ic.routeFactories[ns] = rf
			synced = append(synced, register(rf.Route().V1().Routes().Informer()))
		}
	}
	if c.scope.All {
		nsInformer := ic.factories[metav1.NamespaceAll].Core().V1().Namespaces()
		synced = append(synced, register(nsInformer.Informer()))
		ic.namespaces = nsInformer.Lister()
	}

	for ns := range ic.factories {
		ic.factories[ns].Start(ctx.Done())
	}
	for ns := range ic.routeFactories {
		ic.routeFactories[ns].Start(ctx.Done())
	}

	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !toolscache.WaitForCacheSync(syncCtx.Done(), synced...) {
		return fmt.Errorf("caches for %s did not sync within %s (check RBAC permissions)", c.scope, timeout)
	}
	c.cache = ic
	logger.Printf("Watching %s", c.scope)
	return nil
}

// register strips managedFields (often the largest part of an object) to keep the cache small.
func register(informer toolscache.SharedIndexInformer) toolscache.InformerSynced {
	_ = informer.SetTransform(func(obj interface{}) (interface{}, error) {
		if accessor, ok := obj.(metav1.ObjectMetaAccessor); ok {
			accessor.GetObjectMeta().SetManagedFields(nil)
		}
		return obj, nil
	})
	return informer.HasSynced
}

func (c *Client) routesServed() bool {
	if c.RouteClientSet == nil {
		return false
	}
	_, err := c.Clientset.Discovery().ServerResourcesForGroupVersion("route.openshift.io/v1")
	return err == nil
}

// canList asks the API server whether Smart Proxy may list and watch a resource.
func (c *Client) canList(ctx context.Context, group, resource, namespace string) bool {
	for _, verb := range []string{"list", "watch"} {
		review, err := c.Clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: namespace, Group: group, Resource: resource, Verb: verb,
				},
			},
		}, metav1.CreateOptions{})
		if err != nil || !review.Status.Allowed {
			return false
		}
	}
	return true
}

func nsLabel(ns string) string {
	if ns == metav1.NamespaceAll {
		return "all namespaces"
	}
	return "namespace " + ns
}

// Watches reports whether a namespace is managed by Smart Proxy.
func (c *Client) Watches(namespace string) bool {
	if namespace == "" {
		return false
	}
	if !c.scope.All {
		for _, ns := range c.scope.Namespaces {
			if ns == namespace {
				return true
			}
		}
		return false
	}
	if c.scope.Selector == nil {
		return true
	}
	if c.cache == nil {
		return false
	}
	ns, err := c.cache.namespaces.Get(namespace)
	return err == nil && c.scope.Selector.Matches(labels.Set(ns.Labels))
}

// WatchedNamespaces lists the managed namespaces, sorted.
func (c *Client) WatchedNamespaces() []string {
	if !c.scope.All {
		return append([]string(nil), c.scope.Namespaces...)
	}
	if c.cache == nil {
		return nil
	}
	selector := c.scope.Selector
	if selector == nil {
		selector = labels.Everything()
	}
	list, err := c.cache.namespaces.List(selector)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(list))
	for _, ns := range list {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names
}

// RoutesEnabled reports whether OpenShift Routes are available and watched.
func (c *Client) RoutesEnabled() bool {
	return c.cache != nil && c.cache.routesEnabled
}

// errNotWatched is returned for reads in namespaces Smart Proxy doesn't manage.
var errNotWatched = errors.New("namespace is not watched by Smart Proxy")

// NotWatchedError reports whether err came from accessing an unmanaged namespace.
func NotWatchedError(err error) bool {
	return errors.Is(err, errNotWatched)
}

func (c *Client) factory(namespace string) (informers.SharedInformerFactory, error) {
	if c.cache == nil {
		return nil, errors.New("Kubernetes caches not started")
	}
	if !c.Watches(namespace) {
		return nil, fmt.Errorf("%w: %q", errNotWatched, namespace)
	}
	if c.scope.All {
		return c.cache.factories[metav1.NamespaceAll], nil
	}
	return c.cache.factories[namespace], nil
}

func (c *Client) deployments(namespace string) (appslisters.DeploymentNamespaceLister, error) {
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	return f.Apps().V1().Deployments().Lister().Deployments(namespace), nil
}

func (c *Client) services(namespace string) (corelisters.ServiceNamespaceLister, error) {
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	return f.Core().V1().Services().Lister().Services(namespace), nil
}

func (c *Client) ingresses(namespace string) (networkinglisters.IngressNamespaceLister, error) {
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	return f.Networking().V1().Ingresses().Lister().Ingresses(namespace), nil
}

func (c *Client) hpas(namespace string) (autoscalinglisters.HorizontalPodAutoscalerNamespaceLister, error) {
	if c.cache != nil && !c.cache.hpaEnabled {
		return nil, errors.New("HorizontalPodAutoscalers are not watched (missing RBAC permission)")
	}
	f, err := c.factory(namespace)
	if err != nil {
		return nil, err
	}
	return f.Autoscaling().V2().HorizontalPodAutoscalers().Lister().HorizontalPodAutoscalers(namespace), nil
}

func (c *Client) routes(namespace string) (routelisters.RouteNamespaceLister, error) {
	if !c.RoutesEnabled() {
		return nil, errors.New("OpenShift Routes are not available")
	}
	if !c.Watches(namespace) {
		return nil, fmt.Errorf("%w: %q", errNotWatched, namespace)
	}
	key := namespace
	if c.scope.All {
		key = metav1.NamespaceAll
	}
	return c.cache.routeFactories[key].Route().V1().Routes().Lister().Routes(namespace), nil
}

// notFound builds the error a lister returns, for lookups that end up empty.
func notFound(resource, name string) error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: resource}, name)
}
