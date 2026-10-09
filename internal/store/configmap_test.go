package store

import (
	"context"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// versioned is a fake clientset that numbers resourceVersions like the API server does.
func versioned() *fake.Clientset {
	client := fake.NewClientset()
	version := 0
	stamp := func(action k8stesting.Action) (bool, runtime.Object, error) {
		version++
		obj := action.(interface{ GetObject() runtime.Object }).GetObject().(metav1.Object)
		obj.SetResourceVersion(strconv.Itoa(version))
		return false, nil, nil
	}
	client.PrependReactor("create", "configmaps", stamp)
	client.PrependReactor("update", "configmaps", stamp)
	return client
}

func TestConfigMapDeletedWhileRunningKeepsAllRoutes(t *testing.T) {
	client := fake.NewClientset()
	s := NewStoreWithBackend(NewConfigMapBackend(client, "proxy", "sp-routes", "sp", nil))
	s.AddRoute(&RouteConfig{ID: "a", Namespace: "x"})
	s.AddRoute(&RouteConfig{ID: "b", Namespace: "x"})

	client.CoreV1().ConfigMaps("proxy").Delete(context.TODO(), "sp-routes", metav1.DeleteOptions{})
	s.AddRoute(&RouteConfig{ID: "c", Namespace: "x"})

	routes, _ := NewConfigMapBackend(client, "proxy", "sp-routes", "sp", nil).Load()
	if len(routes) != 3 || len(s.GetAllRoutes()) != 3 {
		t.Fatalf("re-created ConfigMap holds %d routes, store %d; want 3", len(routes), len(s.GetAllRoutes()))
	}
}

func TestStaleWatchEventsAreNotAdopted(t *testing.T) {
	client := versioned()
	s := NewStoreWithBackend(NewConfigMapBackend(client, "proxy", "sp-routes", "sp", nil))
	s.AddRoute(&RouteConfig{ID: "a", Namespace: "x"})
	before, _ := client.CoreV1().ConfigMaps("proxy").Get(context.TODO(), "sp-routes", metav1.GetOptions{})
	s.RemoveRoute("a")
	after, _ := client.CoreV1().ConfigMaps("proxy").Get(context.TODO(), "sp-routes", metav1.GetOptions{})

	// The watch delivers the version with "a" only now, after it was removed here.
	old, _ := DecodeRoutes([]byte(before.Data[ConfigMapKey]))
	s.Adopt(before.ResourceVersion, old)
	if _, ok := s.GetRoute("a"); ok {
		t.Fatal("a stale version brought a removed route back")
	}
	s.Adopt(after.ResourceVersion, nil)
	s.Adopt("newer", old) // Written by another replica afterwards
	if _, ok := s.GetRoute("a"); !ok {
		t.Fatal("a newer version was ignored")
	}
}
