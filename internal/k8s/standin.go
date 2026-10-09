package k8s

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/informers"
	toolscache "k8s.io/client-go/tools/cache"

	"smart-proxy/internal/logger"
)

// Ingresses and Routes can only send traffic to a Service of their own namespace. In every
// namespace where Smart Proxy patches them (other than its own), it keeps a stand-in: a Service
// with Smart Proxy's name and a "proxy" port, without selector, whose endpoints are Smart
// Proxy's ready pods. Ingress controllers and the OpenShift router send traffic straight to
// those pods. Every replica keeps them up to date as Smart Proxy's pods come and go.

// Labels of the stand-ins.
const (
	labelInstance    = "smart-proxy/instance"
	labelComponent   = "smart-proxy/component"
	componentStandIn = "stand-in"
)

type standIns struct {
	c        *Client
	service  string // Smart Proxy's Service, in its own namespace
	instance string

	mu      sync.Mutex
	ips     []string // Ready Smart Proxy pods
	port    int32    // Their proxy port
	written map[string]written
}

// written is the last stand-in written in a namespace, so unchanged ones aren't re-read from
// the API on every pass (they are checked again every few minutes, in case they were removed).
type written struct {
	subsets string
	at      time.Time
}

// EnableStandIns starts keeping stand-ins for Smart Proxy's Service (in its own namespace)
// wherever resources are patched to point at it. Call after Start.
func (c *Client) EnableStandIns(ctx context.Context, service, instance string) error {
	s := &standIns{c: c, service: service, instance: instance, written: map[string]written{}}
	factory := informers.NewSharedInformerFactoryWithOptions(c.Clientset, 0, informers.WithNamespace(c.ownNamespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("metadata.name", service).String()
		}))
	informer := factory.Core().V1().Endpoints().Informer()
	onChange := func(obj any) {
		if ep, ok := obj.(*corev1.Endpoints); ok && s.setAddresses(ep) {
			s.syncAll()
		}
	}
	informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    onChange,
		UpdateFunc: func(_, obj any) { onChange(obj) },
	})
	factory.Start(ctx.Done())
	if !toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("watching the endpoints of %s/%s did not sync", c.ownNamespace, service)
	}
	c.standIns = s
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.syncAll()
			}
		}
	}()
	s.syncAll()
	return nil
}

// setAddresses adopts Smart Proxy's ready endpoints; it reports whether they changed.
func (s *standIns) setAddresses(ep *corev1.Endpoints) bool {
	var ips []string
	var port int32
	for _, subset := range ep.Subsets {
		for _, p := range subset.Ports {
			if p.Name == ProxyPortName {
				port = p.Port
			}
		}
		for _, a := range subset.Addresses {
			ips = append(ips, a.IP)
		}
	}
	sort.Strings(ips)
	s.mu.Lock()
	defer s.mu.Unlock()
	if port == 0 {
		port = s.port // No ready pod: keep the last known port
	}
	if reflect.DeepEqual(ips, s.ips) && port == s.port {
		return false
	}
	s.ips, s.port = ips, port
	return true
}

func (s *standIns) desired(namespace string) (*corev1.Service, *corev1.Endpoints) {
	s.mu.Lock()
	ips, port := s.ips, s.port
	s.mu.Unlock()
	meta := metav1.ObjectMeta{Name: s.service, Namespace: namespace, Labels: map[string]string{
		labelInstance: s.instance, labelComponent: componentStandIn, "app.kubernetes.io/managed-by": "smart-proxy",
	}}
	svc := &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
		Name: ProxyPortName, Port: 80, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP,
	}}}}
	ep := &corev1.Endpoints{ObjectMeta: *meta.DeepCopy()}
	if len(ips) > 0 && port > 0 {
		subset := corev1.EndpointSubset{Ports: []corev1.EndpointPort{{Name: ProxyPortName, Port: port, Protocol: corev1.ProtocolTCP}}}
		for _, ip := range ips {
			subset.Addresses = append(subset.Addresses, corev1.EndpointAddress{IP: ip})
		}
		ep.Subsets = []corev1.EndpointSubset{subset}
	}
	return svc, ep
}

// ensure creates or updates the stand-in of a namespace.
func (s *standIns) ensure(namespace string) error {
	if namespace == s.c.ownNamespace {
		return nil // The real Service is there
	}
	ctx, cancel := apiContext()
	defer cancel()
	svc, ep := s.desired(namespace)
	fingerprint := fmt.Sprint(ep.Subsets)
	s.mu.Lock()
	last := s.written[namespace]
	s.mu.Unlock()
	done := last.subsets == fingerprint && time.Since(last.at) < 5*time.Minute
	if done {
		return nil
	}

	services := s.c.Clientset.CoreV1().Services(namespace)
	existing, err := services.Get(ctx, s.service, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := services.Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Service %s/%s: %w", namespace, s.service, err)
		}
	case err != nil:
		return err
	case existing.Labels[labelComponent] != componentStandIn || existing.Labels[labelInstance] != s.instance:
		return fmt.Errorf("Service %s/%s exists and isn't Smart Proxy's: patched resources there can't reach Smart Proxy", namespace, s.service)
	}

	endpoints := s.c.Clientset.CoreV1().Endpoints(namespace)
	current, err := endpoints.Get(ctx, s.service, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = endpoints.Create(ctx, ep, metav1.CreateOptions{})
	case err == nil && !reflect.DeepEqual(current.Subsets, ep.Subsets):
		current.Subsets = ep.Subsets
		_, err = endpoints.Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("updating the endpoints of %s/%s: %w", namespace, s.service, err)
	}
	s.mu.Lock()
	s.written[namespace] = written{subsets: fingerprint, at: time.Now()}
	s.mu.Unlock()
	return nil
}

// syncAll keeps a stand-in in each watched namespace with resources patched to Smart Proxy, and
// removes those no longer needed.
func (s *standIns) syncAll() {
	needed := map[string]bool{}
	if ings, err := s.c.ListIngresses(); err == nil {
		for _, ing := range ings {
			if IsIngressPatched(ing, s.service) {
				needed[ing.Namespace] = true
			}
		}
	}
	if routes, err := s.c.ListRoutes(); err == nil {
		for _, rt := range routes {
			if IsRoutePatched(rt, s.service) {
				needed[rt.Namespace] = true
			}
		}
	}
	for ns := range needed {
		if !s.c.hasStandIn(ns, s.instance) {
			s.mu.Lock()
			delete(s.written, ns) // Removed by someone: write it again
			s.mu.Unlock()
		}
		if err := s.ensure(ns); err != nil {
			logger.Every("stand-in "+ns, 10*time.Minute, "Warning: %v", err)
		}
	}
	for _, ns := range s.c.WatchedNamespaces() {
		if needed[ns] || ns == s.c.ownNamespace {
			continue
		}
		if s.c.hasStandIn(ns, s.instance) {
			if err := s.c.deleteStandIn(ns, s.service); err != nil {
				logger.Every("stand-in "+ns, 10*time.Minute, "Warning: removing the unused stand-in Service in %s: %v", ns, err)
			} else {
				s.mu.Lock()
				delete(s.written, ns)
				s.mu.Unlock()
			}
		}
	}
}

// hasStandIn reports (from the cache) whether a namespace holds a stand-in of this installation.
func (c *Client) hasStandIn(namespace, instance string) bool {
	services, err := c.services(namespace)
	if err != nil {
		return false
	}
	list, err := services.List(labels.SelectorFromSet(labels.Set{labelInstance: instance, labelComponent: componentStandIn}))
	return err == nil && len(list) > 0
}

// deleteStandIn removes a namespace's stand-in, if it is this installation's.
func (c *Client) deleteStandIn(namespace, service string) error {
	if namespace == c.ownNamespace {
		return nil
	}
	ctx, cancel := apiContext()
	defer cancel()
	svc, err := c.Clientset.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if svc.Labels[labelComponent] != componentStandIn || svc.Labels[labelInstance] != service {
		return nil // Not ours
	}
	err = c.Clientset.CoreV1().Services(namespace).Delete(ctx, service, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &svc.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	err = c.Clientset.CoreV1().Endpoints(namespace).Delete(ctx, service, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// EnsureStandIn makes sure resources patched in a namespace can reach Smart Proxy: called
// before patching. A no-op until EnableStandIns.
func (c *Client) EnsureStandIn(namespace string) error {
	if c.standIns == nil {
		return nil
	}
	return c.standIns.ensure(namespace)
}

// RemoveStandIns deletes this installation's stand-ins in every watched namespace (restore).
func (c *Client) RemoveStandIns(service, instance string) (int, error) {
	removed := 0
	for _, ns := range c.WatchedNamespaces() {
		if ns == c.ownNamespace || !c.hasStandIn(ns, instance) {
			continue
		}
		if err := c.deleteStandIn(ns, service); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
