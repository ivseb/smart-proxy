// Package vault keeps Smart Proxy's own secrets in a Secret of its namespace, shared by every
// replica: the token replicas use to ask each other for recorded requests, the key signing
// route login cookies, and the hashed credentials of protected routes. The Secret is created
// on first start and deleted with Smart Proxy's Deployment.
package vault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
)

const (
	keyPeerToken   = "peer-token"
	keyLoginKey    = "login-key"
	keyCredentials = "credentials.json"
)

// Credentials are the hashed secrets protecting one route.
type Credentials struct {
	Users  map[string]string `json:"users,omitempty"`  // Name -> password hash
	Tokens map[string]string `json:"tokens,omitempty"` // Name -> token hash
}

// State is what the Secret holds.
type State struct {
	PeerToken   string
	LoginKey    []byte
	Credentials map[string]Credentials // Route ID ->
}

// Vault is the Secret, watched so every replica sees changes at once.
type Vault struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	Owner     *metav1.OwnerReference // Deleted with it (Smart Proxy's Deployment)

	mu    sync.RWMutex
	state State
	ready bool
}

// Start creates the Secret if needed and watches it. It fails when Smart Proxy may not use it
// (RBAC): features needing it are then unavailable, and say so.
func (v *Vault) Start(ctx context.Context) error {
	if err := v.ensure(ctx); err != nil {
		return err
	}
	factory := informers.NewSharedInformerFactoryWithOptions(v.Client, 0, informers.WithNamespace(v.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("metadata.name", v.Name).String()
		}))
	informer := factory.Core().V1().Secrets().Informer()
	adopt := func(obj any) {
		if secret, ok := obj.(*corev1.Secret); ok {
			v.adopt(secret)
		}
	}
	informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{AddFunc: adopt, UpdateFunc: func(_, obj any) { adopt(obj) }})
	factory.Start(ctx.Done())
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !toolscache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
		return fmt.Errorf("watching Secret %s/%s did not sync", v.Namespace, v.Name)
	}
	return nil
}

// ensure creates the Secret with fresh keys, or adds keys missing from an existing one.
func (v *Vault) ensure(ctx context.Context) error {
	secrets := v.Client.CoreV1().Secrets(v.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := secrets.Get(ctx, v.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: v.Name, Namespace: v.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "smart-proxy"}}}
			if v.Owner != nil {
				secret.OwnerReferences = []metav1.OwnerReference{*v.Owner}
			}
			fillKeys(secret)
			if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(corev1.Resource("secrets"), v.Name, err) // Created by another replica: read it
			} else if err != nil {
				return err
			}
			v.adopt(secret)
			return nil
		}
		if err != nil {
			return err
		}
		if fillKeys(secret) {
			if secret, err = secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		v.adopt(secret)
		return nil
	})
}

// fillKeys generates the keys a Secret lacks; it reports whether it changed it.
func fillKeys(secret *corev1.Secret) bool {
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	changed := false
	for _, key := range []string{keyPeerToken, keyLoginKey} {
		if len(secret.Data[key]) == 0 {
			secret.Data[key] = []byte(Random(32))
			changed = true
		}
	}
	return changed
}

func (v *Vault) adopt(secret *corev1.Secret) {
	state := State{
		PeerToken:   string(secret.Data[keyPeerToken]),
		LoginKey:    secret.Data[keyLoginKey],
		Credentials: map[string]Credentials{},
	}
	if data := secret.Data[keyCredentials]; len(data) > 0 {
		_ = json.Unmarshal(data, &state.Credentials)
	}
	v.mu.Lock()
	v.state, v.ready = state, true
	v.mu.Unlock()
}

// Ready reports whether the Secret is available.
func (v *Vault) Ready() bool {
	if v == nil {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.ready
}

// PeerToken authenticates replicas to each other ("" when unavailable).
func (v *Vault) PeerToken() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state.PeerToken
}

// LoginKey signs route login cookies (nil when unavailable).
func (v *Vault) LoginKey() []byte {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state.LoginKey
}

// Credentials returns a route's credentials.
func (v *Vault) Credentials(routeID string) Credentials {
	if v == nil {
		return Credentials{}
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state.Credentials[routeID]
}

// UpdateCredentials changes a route's credentials in the Secret (nil removes them).
func (v *Vault) UpdateCredentials(ctx context.Context, routeID string, change func(*Credentials)) error {
	if !v.Ready() {
		return fmt.Errorf("Smart Proxy's Secret %s/%s is not available (RBAC: get, create and update Secrets in its namespace)", v.Namespace, v.Name)
	}
	secrets := v.Client.CoreV1().Secrets(v.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := secrets.Get(ctx, v.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		all := map[string]Credentials{}
		if data := secret.Data[keyCredentials]; len(data) > 0 {
			if err := json.Unmarshal(data, &all); err != nil {
				return err
			}
		}
		creds := all[routeID]
		change(&creds)
		if len(creds.Users) == 0 && len(creds.Tokens) == 0 {
			delete(all, routeID)
		} else {
			all[routeID] = creds
		}
		data, err := json.Marshal(all)
		if err != nil {
			return err
		}
		secret.Data[keyCredentials] = data
		updated, err := secrets.Update(ctx, secret, metav1.UpdateOptions{})
		if err == nil {
			v.adopt(updated)
		}
		return err
	})
}

// Random returns n random bytes, URL-safe base64 encoded.
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
