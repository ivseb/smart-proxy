package vault

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestVaultIsSharedByReplicas(t *testing.T) {
	client := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Vault{Client: client, Namespace: "sp", Name: "sp-state"}
	b := &Vault{Client: client, Namespace: "sp", Name: "sp-state"}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if a.PeerToken() == "" || a.PeerToken() != b.PeerToken() || len(a.LoginKey()) == 0 {
		t.Fatal("replicas don't share the generated keys")
	}

	err := a.UpdateCredentials(ctx, "ing-x/web", func(c *Credentials) {
		c.Users = map[string]Secret{"team": {Hash: "hash"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Credentials("ing-x/web").Users["team"].Hash != "hash" {
		t.Fatal("credentials not saved")
	}
	a.UpdateCredentials(ctx, "ing-x/web", func(c *Credentials) { c.Users = nil })
	if len(a.Credentials("ing-x/web").Users) != 0 {
		t.Fatal("credentials not removed")
	}
}

func TestNilVaultIsUnavailable(t *testing.T) {
	var v *Vault
	if v.Ready() || v.PeerToken() != "" || v.LoginKey() != nil {
		t.Fatal("a missing vault looks available")
	}
}
