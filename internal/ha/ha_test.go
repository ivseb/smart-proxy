package ha

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"smart-proxy/internal/store"
)

const ns = "smart-proxy"

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newReplica starts a replica sharing the fake cluster with the others.
func newReplica(t *testing.T, ctx context.Context, client *fake.Clientset, pod string, requests *atomic.Int64) *Replica {
	t.Helper()
	backend := store.NewConfigMapBackend(client, ns, RoutesConfigMap("sp"), "sp", nil)
	r := &Replica{
		Client: client, Namespace: ns, Instance: "sp", PodName: pod,
		Store:            store.NewStoreWithBackend(backend),
		ActivityInterval: 20 * time.Millisecond,
		LocalRequests: func() RequestCounts {
			n := requests.Load()
			return RequestCounts{Total: n, Routes: map[string]int64{"ing-team-a/web": n}}
		},
	}
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReplicasShareRoutesActivityAndRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewClientset()
	var reqA, reqB atomic.Int64
	a := newReplica(t, ctx, client, "sp-a", &reqA)
	b := newReplica(t, ctx, client, "sp-b", &reqB)

	// A route saved through one replica reaches the other.
	if err := a.Store.AddRoute(&store.RouteConfig{ID: "ing-team-a/web", Namespace: "team-a", Deployment: "web"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, ok := b.Store.GetRoute("ing-team-a/web"); return ok }, "route not shared")

	// Traffic seen only by A keeps the route active on B too.
	b.Store.SetActivityForTest("ing-team-a/web", time.Now().Add(-time.Hour))
	a.Store.UpdateActivity("ing-team-a/web")
	reqA.Store(3)
	reqB.Store(2)
	eventually(t, func() bool {
		r, _ := b.Store.GetRoute("ing-team-a/web")
		return time.Since(r.LastActivity) < time.Minute
	}, "activity not shared")

	// Request counts are summed across replicas (B publishes once it has activity of its own).
	b.Store.UpdateActivity("ing-team-a/web")
	eventually(t, func() bool { return a.ClusterRequests().Total == 5 && b.ClusterRequests().Total == 5 }, "request counts not summed")

	// Deleting through B removes it from A.
	if err := b.Store.RemoveRoute("ing-team-a/web"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, ok := a.Store.GetRoute("ing-team-a/web"); return !ok }, "deletion not shared")
}

func TestOnlyOneReplicaLeads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewClientset()

	var mu sync.Mutex
	leaders := map[string]bool{}
	for _, pod := range []string{"sp-a", "sp-b", "sp-c"} {
		r := &Replica{Client: client, Namespace: ns, Instance: "sp", PodName: pod}
		go r.RunLeaderElection(ctx, func(leadCtx context.Context) {
			mu.Lock()
			leaders[pod] = true
			mu.Unlock()
			<-leadCtx.Done()
		})
	}

	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(leaders) > 0 }, "no leader elected")
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(leaders) != 1 {
		t.Fatalf("leaders = %v, want exactly one", leaders)
	}
}
