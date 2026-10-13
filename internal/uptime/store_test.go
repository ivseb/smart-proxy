package uptime

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := &Store{Client: fake.NewClientset(), Namespace: "sp", Name: "sp-uptime"}
	if series, err := s.Load(ctx); err != nil || series != nil {
		t.Fatalf("no ConfigMap yet: %v %v", series, err)
	}
	r := NewRecorder()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	r.Sample(now, []State{{Namespace: "shop", Workload: "web", Replicas: 2}, {Namespace: "shop", Workload: "statefulset/db", Replicas: 0}})
	for i := 0; i < 2; i++ { // Created, then updated
		if err := s.Save(ctx, r.Snapshot(now)); err != nil {
			t.Fatal(err)
		}
	}
	series, err := s.Load(ctx)
	if err != nil || len(series) != 2 || series[1].Workload != "web" || series[1].Replicas[0] != 2 || !series[1].LastUp {
		t.Fatalf("loaded %+v %v", series, err)
	}
}
