package trash

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"smart-proxy/internal/store"
)

func TestConfigMapBin(t *testing.T) {
	ctx := context.Background()
	bins := map[string]Bin{
		"configmap": &ConfigMap{Client: fake.NewClientset(), Namespace: "sp", Name: "sp-deleted-routes"},
		"memory":    &Memory{},
	}
	for name, bin := range bins {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			old := NewEntry(store.RouteConfig{ID: "old"}, "", nil, now.Add(-Retention-time.Hour))
			old.Credentials = true
			// Too old to keep: returned as expired, so its credentials can be forgotten.
			expired, err := bin.Add(ctx, old)
			if err != nil || len(expired) != 1 || expired[0].ID != old.ID || !expired[0].Credentials {
				t.Fatalf("expired = %+v, %v", expired, err)
			}
			route := store.RouteConfig{ID: "ing-shop/web", Host: "web.example.com", IdleTimeout: time.Hour, LastActivity: now}
			entry := NewEntry(route, "mario", []string{"Ingress shop/web"}, now)
			if _, err := bin.Add(ctx, entry); err != nil {
				t.Fatal(err)
			}
			entries, _, _ := bin.List(ctx)
			if len(entries) != 1 || entries[0].Route.IdleTimeout != time.Hour || !entries[0].Route.LastActivity.IsZero() || entries[0].DeletedBy != "mario" {
				t.Fatalf("entries = %+v", entries)
			}
			got, ok, err := bin.Take(ctx, entry.ID)
			if err != nil || !ok || got.Route.Host != "web.example.com" {
				t.Fatalf("take = %+v %v %v", got, ok, err)
			}
			if _, ok, _ := bin.Take(ctx, entry.ID); ok {
				t.Fatal("taken twice")
			}

			// Bounded: the oldest go first.
			for i := 0; i < MaxEntries+5; i++ {
				bin.Add(ctx, NewEntry(store.RouteConfig{ID: fmt.Sprintf("r%d", i)}, "", nil, now.Add(time.Duration(i)*time.Second)))
			}
			entries, _, _ = bin.List(ctx)
			if len(entries) != MaxEntries || entries[0].Route.ID != fmt.Sprintf("r%d", MaxEntries+4) {
				t.Fatalf("%d entries, newest %s", len(entries), entries[0].Route.ID)
			}
		})
	}
}
