// Package fakecluster builds a k8s.Client on fake clientsets for tests: Kubernetes and
// OpenShift objects, the Routes API advertised by discovery, and RBAC that allows everything
// unless told otherwise.
package fakecluster

import (
	"context"
	"testing"
	"time"

	appsv1openshift "github.com/openshift/api/apps/v1"
	routev1 "github.com/openshift/api/route/v1"
	appsfake "github.com/openshift/client-go/apps/clientset/versioned/fake"
	routefake "github.com/openshift/client-go/route/clientset/versioned/fake"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"smart-proxy/internal/k8s"
)

// Options tune the fake cluster.
type Options struct {
	// NoRoutesAPI makes the cluster vanilla Kubernetes (no route.openshift.io, no apps.openshift.io).
	NoRoutesAPI bool
	// Deny lists "group/resource" pairs RBAC refuses to list, e.g. "autoscaling/horizontalpodautoscalers".
	Deny []string
}

// Cluster is a started k8s.Client plus direct access to the fakes behind it.
type Cluster struct {
	*k8s.Client
	Kube   *fake.Clientset
	Routes *routefake.Clientset
	Apps   *appsfake.Clientset
}

// New starts a client for scope over the given objects (Kubernetes objects and *routev1.Route).
func New(t *testing.T, scope k8s.Scope, opts Options, objects ...runtime.Object) *Cluster {
	t.Helper()
	var kubeObjs, routeObjs, appsObjs []runtime.Object
	for _, o := range objects {
		switch o.(type) {
		case *routev1.Route:
			routeObjs = append(routeObjs, o)
		case *appsv1openshift.DeploymentConfig:
			appsObjs = append(appsObjs, o)
		default:
			kubeObjs = append(kubeObjs, o)
		}
	}

	kube := fake.NewClientset(kubeObjs...)
	routes := routefake.NewClientset(routeObjs...)
	apps := appsfake.NewClientset(appsObjs...)

	denied := map[string]bool{}
	for _, d := range opts.Deny {
		denied[d] = true
	}
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		review.Status.Allowed = !denied[attrs.Group+"/"+attrs.Resource]
		return true, review, nil
	})
	if !opts.NoRoutesAPI {
		kube.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{
			GroupVersion: "route.openshift.io/v1",
			APIResources: []metav1.APIResource{{Name: "routes", Namespaced: true, Kind: "Route"}},
		}, {
			GroupVersion: "apps.openshift.io/v1",
			APIResources: []metav1.APIResource{{Name: "deploymentconfigs", Namespaced: true, Kind: "DeploymentConfig"}},
		}}
	}

	own := "smart-proxy"
	if len(scope.Namespaces) > 0 {
		own = scope.Namespaces[0]
	}
	client := k8s.NewWithClients(kube, routes, apps, scope, own)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := client.Start(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return &Cluster{Client: client, Kube: kube, Routes: routes, Apps: apps}
}

// Eventually retries cond until it holds or a second passes: writes reach the informer
// caches asynchronously.
func Eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
