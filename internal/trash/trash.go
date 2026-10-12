// Package trash keeps deleted routes for a while, so a route deleted by mistake can be restored
// with all its settings. Entries are kept 30 days (the latest 50), in a ConfigMap of Smart
// Proxy's namespace, shared by every replica and removed with Smart Proxy.
package trash

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"smart-proxy/internal/store"
)

const (
	// Retention is how long deleted routes are kept.
	Retention = 30 * 24 * time.Hour
	// MaxEntries bounds the bin: the oldest go first.
	MaxEntries = 50
	key        = "deleted.json"
)

// Entry is a deleted route.
type Entry struct {
	ID        string            `json:"id"` // The route's ID and when it was deleted
	Route     store.RouteConfig `json:"route"`
	DeletedAt time.Time         `json:"deleted_at"`
	DeletedBy string            `json:"deleted_by,omitempty"`
	// Resources are the Ingresses/Routes that pointed at Smart Proxy for it, e.g. "Ingress shop/web".
	Resources []string `json:"resources,omitempty"`
	// Credentials is true when the route's people and tokens are kept for a restore.
	Credentials bool `json:"credentials,omitempty"`
}

// NewEntry describes a route about to be deleted.
func NewEntry(route store.RouteConfig, by string, resources []string, now time.Time) Entry {
	route.LastActivity, route.InspectUntil, route.Pause = time.Time{}, nil, nil
	return Entry{ID: route.ID + "@" + strconv.FormatInt(now.UnixMilli(), 10), Route: route, DeletedAt: now, DeletedBy: by, Resources: resources}
}

// CredentialsKey is where a deleted route's credentials wait in Smart Proxy's Secret.
func (e Entry) CredentialsKey() string { return "deleted:" + e.ID }

// Bin holds the deleted routes. Add and List also return the entries that just expired, whose
// credentials the caller forgets.
type Bin interface {
	Add(ctx context.Context, e Entry) (expired []Entry, err error)
	List(ctx context.Context) (entries, expired []Entry, err error)
	Take(ctx context.Context, id string) (Entry, bool, error)
}

// prune sorts entries newest first and splits off those too old or beyond MaxEntries.
func prune(entries []Entry, now time.Time) (kept, expired []Entry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].DeletedAt.After(entries[j].DeletedAt) })
	for _, e := range entries {
		if now.Sub(e.DeletedAt) > Retention || len(kept) >= MaxEntries {
			expired = append(expired, e)
		} else {
			kept = append(kept, e)
		}
	}
	return kept, expired
}

// Memory is a Bin for Smart Proxy running without Kubernetes (tests, local runs).
type Memory struct {
	mu      sync.Mutex
	entries []Entry
}

func (m *Memory) Add(_ context.Context, e Entry) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var expired []Entry
	m.entries, expired = prune(append(m.entries, e), time.Now())
	return expired, nil
}

func (m *Memory) List(_ context.Context) ([]Entry, []Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var expired []Entry
	m.entries, expired = prune(m.entries, time.Now())
	return append([]Entry(nil), m.entries...), expired, nil
}

func (m *Memory) Take(_ context.Context, id string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.entries {
		if e.ID == id {
			m.entries = append(m.entries[:i:i], m.entries[i+1:]...)
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// ConfigMap is a Bin kept in a ConfigMap, created on first use.
type ConfigMap struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	Labels    map[string]string
	Owner     *metav1.OwnerReference // Deleted with it (Smart Proxy's Deployment)
}

func (c *ConfigMap) read(ctx context.Context) (*corev1.ConfigMap, []Entry, error) {
	cm, err := c.Client.CoreV1().ConfigMaps(c.Namespace).Get(ctx, c.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var entries []Entry
	if data := cm.Data[key]; data != "" {
		if err := json.Unmarshal([]byte(data), &entries); err != nil {
			return nil, nil, err
		}
	}
	return cm, entries, nil
}

// update applies a change to the entries, retrying on conflicts with other replicas.
func (c *ConfigMap) update(ctx context.Context, change func([]Entry) []Entry) (expired []Entry, err error) {
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, entries, err := c.read(ctx)
		if err != nil {
			return err
		}
		var kept []Entry
		kept, expired = prune(change(entries), time.Now())
		data, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		if cm == nil {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: c.Namespace, Labels: c.Labels}}
			if c.Owner != nil {
				cm.OwnerReferences = []metav1.OwnerReference{*c.Owner}
			}
			cm.Data = map[string]string{key: string(data)}
			_, err = c.Client.CoreV1().ConfigMaps(c.Namespace).Create(ctx, cm, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(corev1.Resource("configmaps"), c.Name, err)
			}
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = string(data)
		_, err = c.Client.CoreV1().ConfigMaps(c.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
	return expired, err
}

func (c *ConfigMap) Add(ctx context.Context, e Entry) ([]Entry, error) {
	return c.update(ctx, func(entries []Entry) []Entry { return append(entries, e) })
}

func (c *ConfigMap) List(ctx context.Context) ([]Entry, []Entry, error) {
	_, entries, err := c.read(ctx)
	if err != nil {
		return nil, nil, err
	}
	kept, expired := prune(entries, time.Now())
	if len(expired) > 0 {
		if _, err := c.update(ctx, func(e []Entry) []Entry { return e }); err != nil {
			return kept, nil, nil // Pruned next time
		}
	}
	return kept, expired, nil
}

func (c *ConfigMap) Take(ctx context.Context, id string) (Entry, bool, error) {
	var taken Entry
	found := false
	_, err := c.update(ctx, func(entries []Entry) []Entry {
		found = false
		out := entries[:0:0]
		for _, e := range entries {
			if e.ID == id && !found {
				taken, found = e, true
				continue
			}
			out = append(out, e)
		}
		return out
	})
	return taken, found && err == nil, err
}
