// Package ha lets several Smart Proxy replicas work as one: they share route configurations
// through a ConfigMap, share request activity through one small ConfigMap per pod, and elect a
// leader that alone puts deployments to sleep and heals patches.
package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

const (
	activityKey = "activity.json"
	requestsKey = "requests.json"
	trafficKey  = "traffic.json"

	// Traffic sources change with every request; share them less often than activity.
	trafficEvery = 4
)

// RequestCounts are proxied request totals, overall and per route.
type RequestCounts struct {
	Total  int64            `json:"total"`
	Routes map[string]int64 `json:"routes"`
}

// Replica is this Smart Proxy pod.
type Replica struct {
	Client    kubernetes.Interface
	Namespace string // Smart Proxy's own namespace
	Instance  string // Name shared by all replicas of one installation (the Service name)
	PodName   string
	PodUID    string // Owner of the activity ConfigMap, so it is deleted with the pod
	Store     *store.Store

	// LocalRequests returns this pod's request counts, published with its activity so the
	// dashboard can show totals across replicas.
	LocalRequests func() RequestCounts
	// LocalTraffic returns who sent requests to each route through this pod.
	LocalTraffic func() map[string][]traffic.SourceStats

	// ActivityInterval is how often local activity is published (and others' merged).
	ActivityInterval time.Duration

	configMaps corelisters.ConfigMapNamespaceLister
}

// RoutesConfigMap is the name of the ConfigMap holding the routes of an installation.
func RoutesConfigMap(instance string) string { return instance + "-routes" }

func (r *Replica) activityConfigMap() string { return r.Instance + "-activity-" + r.PodName }

func (r *Replica) selector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{store.LabelInstance: r.Instance})
}

// Start watches the installation's ConfigMaps, adopting route changes made by other replicas,
// and periodically exchanges request activity with them. It returns once the watch is synced.
func (r *Replica) Start(ctx context.Context) error {
	if r.ActivityInterval == 0 {
		r.ActivityInterval = 15 * time.Second
	}
	factory := informers.NewSharedInformerFactoryWithOptions(r.Client, 0,
		informers.WithNamespace(r.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = r.selector().String() }))
	informer := factory.Core().V1().ConfigMaps()
	r.configMaps = informer.Lister().ConfigMaps(r.Namespace)

	onRoutes := func(obj interface{}) {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Name != RoutesConfigMap(r.Instance) {
			return
		}
		routes, err := store.DecodeRoutes([]byte(cm.Data[store.ConfigMapKey]))
		if err != nil {
			logger.Printf("Warning: ignoring invalid routes in ConfigMap %s: %v", cm.Name, err)
			return
		}
		r.Store.Replace(routes)
	}
	informer.Informer().AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    onRoutes,
		UpdateFunc: func(_, obj interface{}) { onRoutes(obj) },
	})

	factory.Start(ctx.Done())
	if !toolscache.WaitForCacheSync(ctx.Done(), informer.Informer().HasSynced) {
		return fmt.Errorf("watching ConfigMaps in %s did not sync", r.Namespace)
	}
	go r.exchangeActivity(ctx)
	return nil
}

// exchangeActivity publishes this pod's activity and merges the other pods'.
func (r *Replica) exchangeActivity(ctx context.Context) {
	ticker := time.NewTicker(r.ActivityInterval)
	defer ticker.Stop()
	var published map[string]time.Time
	var publishedRequests RequestCounts
	var sources string
	for tick := 0; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		local := r.Store.LocalActivity()
		requests := r.localRequests()
		if tick%trafficEvery == 0 && r.LocalTraffic != nil {
			if data, err := json.Marshal(r.LocalTraffic()); err == nil {
				sources = string(data)
			}
		}
		if len(local) > 0 && (!reflect.DeepEqual(local, published) || !reflect.DeepEqual(requests, publishedRequests)) {
			if err := r.publish(ctx, local, requests, sources); err != nil {
				logger.Printf("Warning: failed to share request activity: %v", err)
			} else {
				published, publishedRequests = local, requests
			}
		}
		r.Store.MergeActivity(r.othersActivity())
	}
}

func (r *Replica) localRequests() RequestCounts {
	if r.LocalRequests == nil {
		return RequestCounts{Routes: map[string]int64{}}
	}
	return r.LocalRequests()
}

// ClusterRequests adds this pod's request counts to the latest published by the others.
func (r *Replica) ClusterRequests() RequestCounts {
	total := r.localRequests()
	sum := RequestCounts{Total: total.Total, Routes: map[string]int64{}}
	for id, n := range total.Routes {
		sum.Routes[id] = n
	}
	for _, cm := range r.otherActivityConfigMaps() {
		var counts RequestCounts
		if json.Unmarshal([]byte(cm.Data[requestsKey]), &counts) != nil {
			continue
		}
		sum.Total += counts.Total
		for id, n := range counts.Routes {
			sum.Routes[id] += n
		}
	}
	return sum
}

func (r *Replica) publish(ctx context.Context, activity map[string]time.Time, requests RequestCounts, sources string) error {
	data, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	requestData, err := json.Marshal(requests)
	if err != nil {
		return err
	}
	cms := r.Client.CoreV1().ConfigMaps(r.Namespace)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.activityConfigMap(),
			Namespace: r.Namespace,
			Labels:    map[string]string{store.LabelInstance: r.Instance, store.LabelComponent: store.ComponentActivity},
		},
		Data: map[string]string{activityKey: string(data), requestsKey: string(requestData), trafficKey: sources},
	}
	if r.PodUID != "" {
		// Deleted by Kubernetes together with the pod.
		cm.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: r.PodName, UID: types.UID(r.PodUID)}}
	}
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
	}
	return err
}

// ClusterTraffic merges who sent requests to a route through this pod and the others.
func (r *Replica) ClusterTraffic(routeID string) []traffic.SourceStats {
	var lists [][]traffic.SourceStats
	if r.LocalTraffic != nil {
		lists = append(lists, r.LocalTraffic()[routeID])
	}
	for _, cm := range r.otherActivityConfigMaps() {
		var all map[string][]traffic.SourceStats
		if json.Unmarshal([]byte(cm.Data[trafficKey]), &all) == nil {
			lists = append(lists, all[routeID])
		}
	}
	return traffic.Merge(lists...)
}

// otherActivityConfigMaps are the activity ConfigMaps of the other replicas.
func (r *Replica) otherActivityConfigMaps() []*corev1.ConfigMap {
	if r.configMaps == nil {
		return nil
	}
	list, err := r.configMaps.List(labels.SelectorFromSet(labels.Set{
		store.LabelInstance: r.Instance, store.LabelComponent: store.ComponentActivity,
	}))
	if err != nil {
		return nil
	}
	others := list[:0:0]
	for _, cm := range list {
		if cm.Name != r.activityConfigMap() {
			others = append(others, cm)
		}
	}
	return others
}

// othersActivity merges the activity published by every other replica.
func (r *Replica) othersActivity() map[string]time.Time {
	merged := map[string]time.Time{}
	for _, cm := range r.otherActivityConfigMaps() {
		var activity map[string]time.Time
		if json.Unmarshal([]byte(cm.Data[activityKey]), &activity) != nil {
			continue
		}
		for id, t := range activity {
			if t.After(merged[id]) {
				merged[id] = t
			}
		}
	}
	return merged
}

// RunLeaderElection calls lead whenever this replica becomes the leader; lead's context is
// cancelled when leadership is lost. It returns when ctx is cancelled.
func (r *Replica) RunLeaderElection(ctx context.Context, lead func(ctx context.Context)) {
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: r.Instance + "-leader", Namespace: r.Namespace},
		Client:     r.Client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: r.PodName},
	}
	for ctx.Err() == nil {
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   15 * time.Second,
			RenewDeadline:   10 * time.Second,
			RetryPeriod:     2 * time.Second,
			ReleaseOnCancel: true,
			Name:            r.Instance,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leadCtx context.Context) {
					logger.Printf("This replica (%s) is now the leader: it handles sleeping and self-healing", r.PodName)
					lead(leadCtx)
				},
				OnStoppedLeading: func() {
					logger.Printf("Replica %s is no longer the leader", r.PodName)
				},
				OnNewLeader: func(identity string) {
					if identity != r.PodName {
						logger.Printf("Leader is %s", identity)
					}
				},
			},
		})
	}
}
