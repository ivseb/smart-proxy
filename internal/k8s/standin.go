package k8s

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
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
// those pods (Kubernetes mirrors the Endpoints into EndpointSlices).
//
// The leader keeps them in line with Smart Proxy's pods and removes those no longer needed;
// any replica creates one when it patches a resource in a namespace without it.

// Labels of the stand-ins: which installation (Service name and namespace) they belong to.
const (
	labelInstance    = "smart-proxy/instance"
	labelNamespace   = "smart-proxy/namespace"
	labelComponent   = "smart-proxy/component"
	componentStandIn = "stand-in"
)

type standIns struct {
	c       *Client
	service string // Smart Proxy's Service, in its own namespace

	leading atomic.Bool
	trigger chan struct{} // Coalesced requests for a pass

	mu       sync.Mutex
	subsets  []corev1.EndpointSubset // Smart Proxy's ready pods, proxy port only
	written  map[string]written      // Namespace -> last stand-in written there
	unneeded map[string]int          // Namespace -> passes in a row its stand-in was unneeded
}

// written is the last stand-in written in a namespace, so unchanged ones aren't re-read from
// the API on every pass (they are checked again every few minutes, in case they were changed).
type written struct {
	subsets string
	at      time.Time
}

// errNoStandIns is returned when patching outside Smart Proxy's namespace without stand-ins.
var errNoStandIns = errors.New("cannot reach Smart Proxy from other namespaces: it may not read its own Service's endpoints (RBAC: get, list and watch endpoints in its namespace)")

// EnableStandIns starts keeping stand-ins of Smart Proxy's Service (in its own namespace)
// wherever resources are patched to point at it. Call after Start. Without permission to read
// its own endpoints, it logs why and patching outside its namespace is refused.
func (c *Client) EnableStandIns(ctx context.Context, service string) {
	s := &standIns{c: c, service: service, trigger: make(chan struct{}, 1), written: map[string]written{}, unneeded: map[string]int{}}
	c.standIns = s
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	allowed := c.canList(checkCtx, "", "endpoints", c.ownNamespace)
	cancel()
	if !allowed {
		c.standInsErr = errNoStandIns
		logger.Printf("Warning: %v. Ingresses and Routes in other namespaces can't be patched.", errNoStandIns)
		return
	}

	factory := informers.NewSharedInformerFactoryWithOptions(c.Clientset, 0, informers.WithNamespace(c.ownNamespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("metadata.name", service).String()
		}))
	informer := factory.Core().V1().Endpoints().Informer()
	onChange := func(obj any) {
		if ep, ok := obj.(*corev1.Endpoints); ok && s.setSubsets(ep) {
			s.requestPass()
		}
	}
	informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    onChange,
		UpdateFunc: func(_, obj any) { onChange(obj) },
	})
	factory.Start(ctx.Done())
	// Know Smart Proxy's pods before writing any stand-in (bounded: permission was checked).
	syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
	defer cancelSync()
	if !toolscache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
		logger.Printf("Warning: Smart Proxy's own endpoints are not known yet; stand-ins will follow when they are")
	}
	go s.run(ctx)
}

// SetLeading tells the stand-ins whether this replica leads: only the leader updates and removes
// them, so replicas never undo each other's writes.
func (c *Client) SetLeading(leading bool) {
	if s := c.standIns; s != nil {
		s.leading.Store(leading)
		if leading {
			s.requestPass()
		}
	}
}

func (s *standIns) requestPass() {
	select {
	case s.trigger <- struct{}{}:
	default: // One already pending
	}
}

// run makes passes, one at a time: when asked, and every 30 seconds.
func (s *standIns) run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.trigger:
		}
		if s.leading.Load() {
			s.syncAll()
		}
	}
}

// setSubsets adopts Smart Proxy's ready endpoints (their "proxy" port); it reports whether they
// changed.
func (s *standIns) setSubsets(ep *corev1.Endpoints) bool {
	var subsets []corev1.EndpointSubset
	for _, subset := range ep.Subsets {
		var port int32
		for _, p := range subset.Ports {
			if p.Name == ProxyPortName {
				port = p.Port
			}
		}
		if port == 0 || len(subset.Addresses) == 0 {
			continue
		}
		mirrored := corev1.EndpointSubset{Ports: []corev1.EndpointPort{{Name: ProxyPortName, Port: port, Protocol: corev1.ProtocolTCP}}}
		for _, a := range subset.Addresses {
			mirrored.Addresses = append(mirrored.Addresses, corev1.EndpointAddress{IP: a.IP})
		}
		sort.Slice(mirrored.Addresses, func(i, j int) bool { return mirrored.Addresses[i].IP < mirrored.Addresses[j].IP })
		subsets = append(subsets, mirrored)
	}
	sort.Slice(subsets, func(i, j int) bool {
		return fmt.Sprint(subsets[i].Ports, subsets[i].Addresses) < fmt.Sprint(subsets[j].Ports, subsets[j].Addresses)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if reflect.DeepEqual(subsets, s.subsets) {
		return false
	}
	s.subsets = subsets
	return true
}

func (s *standIns) labels() map[string]string {
	return map[string]string{
		labelInstance: s.service, labelNamespace: s.c.ownNamespace, labelComponent: componentStandIn,
		"app.kubernetes.io/managed-by": "smart-proxy",
	}
}

func (c *Client) isStandIn(meta metav1.ObjectMeta, service string) bool {
	return meta.Labels[labelComponent] == componentStandIn && meta.Labels[labelInstance] == service &&
		meta.Labels[labelNamespace] == c.ownNamespace
}

// ensure creates or updates the stand-in of a namespace. Unless forced, a stand-in written
// recently with the same endpoints is trusted without asking the API.
func (s *standIns) ensure(namespace string, force bool) error {
	if namespace == s.c.ownNamespace {
		return nil // The real Service is there
	}
	s.mu.Lock()
	subsets := s.subsets
	last := s.written[namespace]
	s.mu.Unlock()
	fingerprint := fmt.Sprint(subsets)
	if !force && last.subsets == fingerprint && time.Since(last.at) < 5*time.Minute {
		return nil
	}

	ctx, cancel := apiContext()
	defer cancel()
	meta := metav1.ObjectMeta{Name: s.service, Namespace: namespace, Labels: s.labels()}
	services := s.c.Clientset.CoreV1().Services(namespace)
	existing, err := services.Get(ctx, s.service, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		svc := &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
			Name: ProxyPortName, Port: 80, TargetPort: intstr.FromString(ProxyPortName), Protocol: corev1.ProtocolTCP,
		}}}}
		if _, err := services.Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Service %s/%s: %w", namespace, s.service, err)
		}
	case err != nil:
		return err
	case !s.c.isStandIn(existing.ObjectMeta, s.service):
		return fmt.Errorf("Service %s/%s exists and isn't this Smart Proxy's: patched resources there can't reach it", namespace, s.service)
	}

	endpoints := s.c.Clientset.CoreV1().Endpoints(namespace)
	for attempt := 0; ; attempt++ {
		current, err := endpoints.Get(ctx, s.service, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, err = endpoints.Create(ctx, &corev1.Endpoints{ObjectMeta: *meta.DeepCopy(), Subsets: subsets}, metav1.CreateOptions{})
		case err == nil && !reflect.DeepEqual(current.Subsets, subsets) && (len(current.Subsets) > 0 || len(subsets) > 0):
			current.Subsets = subsets
			_, err = endpoints.Update(ctx, current, metav1.UpdateOptions{})
		}
		if (apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)) && attempt == 0 {
			continue // Written meanwhile (e.g. by another replica): look again
		}
		if err != nil {
			return fmt.Errorf("updating the endpoints of %s/%s: %w", namespace, s.service, err)
		}
		break
	}
	s.mu.Lock()
	s.written[namespace] = written{subsets: fingerprint, at: time.Now()}
	s.mu.Unlock()
	return nil
}

// needs reports (from the caches) which namespaces have resources patched to Smart Proxy.
func (s *standIns) needs() (map[string]bool, error) {
	needed := map[string]bool{}
	ings, err := s.c.ListIngresses()
	if err != nil {
		return nil, err
	}
	for _, ing := range ings {
		if IsIngressPatched(ing, s.service) {
			needed[ing.Namespace] = true
		}
	}
	routes, err := s.c.ListRoutes()
	if err != nil {
		return nil, err
	}
	for _, rt := range routes {
		if IsRoutePatched(rt, s.service) {
			needed[rt.Namespace] = true
		}
	}
	return needed, nil
}

// syncAll keeps a stand-in in each watched namespace with resources patched to Smart Proxy, and
// removes those no longer needed (after two passes in a row, so one being patched right now
// keeps its stand-in).
func (s *standIns) syncAll() {
	needed, err := s.needs()
	if err != nil {
		return // Not knowing what is needed, change nothing
	}
	for ns := range needed {
		s.mu.Lock()
		delete(s.unneeded, ns)
		if !s.c.hasStandIn(ns, s.service) {
			delete(s.written, ns) // Removed by someone: write it again
		}
		s.mu.Unlock()
		if err := s.ensure(ns, false); err != nil {
			logger.Every("stand-in "+ns, 10*time.Minute, "Warning: %v", err)
		}
	}
	for _, ns := range s.c.WatchedNamespaces() {
		if needed[ns] || ns == s.c.ownNamespace || !s.c.hasStandIn(ns, s.service) {
			continue
		}
		s.mu.Lock()
		s.unneeded[ns]++
		passes := s.unneeded[ns]
		s.mu.Unlock()
		if passes < 2 {
			continue
		}
		if now, err := s.needs(); err != nil || now[ns] {
			continue // Patched meanwhile
		}
		if err := s.c.deleteStandIn(ns, s.service); err != nil {
			logger.Every("stand-in "+ns, 10*time.Minute, "Warning: removing the unused stand-in Service in %s: %v", ns, err)
			continue
		}
		s.mu.Lock()
		delete(s.written, ns)
		delete(s.unneeded, ns)
		s.mu.Unlock()
	}
}

// hasStandIn reports (from the cache) whether a namespace holds a stand-in of this installation.
func (c *Client) hasStandIn(namespace, service string) bool {
	services, err := c.services(namespace)
	if err != nil {
		return false
	}
	list, err := services.List(labels.SelectorFromSet(labels.Set{
		labelInstance: service, labelNamespace: c.ownNamespace, labelComponent: componentStandIn,
	}))
	return err == nil && len(list) > 0
}

// deleteStandIn removes a namespace's stand-in, if it is this installation's: its Endpoints
// first, then the Service, each only if it is still the object that was checked.
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
	if !c.isStandIn(svc.ObjectMeta, service) {
		return nil // Not ours
	}
	if ep, err := c.Clientset.CoreV1().Endpoints(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
		err = c.Clientset.CoreV1().Endpoints(namespace).Delete(ctx, service, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ep.UID}})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return err
		}
	}
	err = c.Clientset.CoreV1().Services(namespace).Delete(ctx, service, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &svc.UID}})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}

// EnsureStandIn makes sure resources patched in a namespace can reach Smart Proxy: called
// right before patching one there.
func (c *Client) EnsureStandIn(namespace string) error {
	if c.standIns == nil || namespace == c.ownNamespace {
		return nil
	}
	if c.standInsErr != nil {
		return c.standInsErr
	}
	return c.standIns.ensure(namespace, true)
}

// RemoveStandIns deletes this installation's stand-ins in every watched namespace (restore).
func (c *Client) RemoveStandIns(service string) (int, error) {
	removed := 0
	for _, ns := range c.WatchedNamespaces() {
		if ns == c.ownNamespace || !c.hasStandIn(ns, service) {
			continue
		}
		if err := c.deleteStandIn(ns, service); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
