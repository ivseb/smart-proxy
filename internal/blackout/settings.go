package blackout

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

const dataKey = "settings.json"

// Keeper holds the settings in force and, with a cluster, keeps them in a ConfigMap shared by
// every replica (Adopt takes the changes made by the others).
type Keeper struct {
	Client    kubernetes.Interface // nil: in memory only
	Namespace string
	Name      string
	Labels    map[string]string
	Owner     *metav1.OwnerReference

	current atomic.Pointer[Settings]
}

// Get returns the settings in force.
func (k *Keeper) Get() Settings {
	if s := k.current.Load(); s != nil {
		return *s
	}
	return Defaults()
}

func (k *Keeper) set(s Settings) {
	if s.Namespaces == nil {
		s.Namespaces = map[string]store.BlackoutPolicy{}
	}
	if s.Holidays == nil {
		s.Holidays = []Holiday{}
	}
	if s.Timezone == "" {
		s.Timezone = DefaultTimezone
	}
	k.current.Store(&s)
}

// Load reads the settings from the ConfigMap (defaults when there is none yet).
func (k *Keeper) Load(ctx context.Context) error {
	k.set(Defaults())
	if k.Client == nil {
		return nil
	}
	cm, err := k.Client.CoreV1().ConfigMaps(k.Namespace).Get(ctx, k.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	k.Adopt(cm)
	return nil
}

// Adopt takes the settings of a version of the ConfigMap (written by any replica).
func (k *Keeper) Adopt(cm *corev1.ConfigMap) {
	if cm.Name != k.Name {
		return
	}
	s, err := decode(cm.Data[dataKey])
	if err != nil {
		logger.Printf("Warning: ignoring invalid settings in ConfigMap %s: %v", cm.Name, err)
		return
	}
	k.set(s)
}

func decode(data string) (Settings, error) {
	s := Defaults()
	if data == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return s, err
	}
	return s, s.Validate()
}

// Save validates and stores new settings.
func (k *Keeper) Save(ctx context.Context, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if k.Client != nil {
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		cms := k.Client.CoreV1().ConfigMaps(k.Namespace)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cm, err := cms.Get(ctx, k.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: k.Name, Namespace: k.Namespace, Labels: k.Labels},
					Data: map[string]string{dataKey: string(data)}}
				if k.Owner != nil {
					cm.OwnerReferences = []metav1.OwnerReference{*k.Owner}
				}
				_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
				return err
			}
			if err != nil {
				return err
			}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[dataKey] = string(data)
			_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return fmt.Errorf("saving the settings: %w", err)
		}
	}
	k.set(s)
	return nil
}
