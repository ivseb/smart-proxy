package store

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// ConfigMapKey holds the routes in the ConfigMap.
const ConfigMapKey = "routes.json"

// Labels on the ConfigMaps Smart Proxy creates for itself.
const (
	LabelComponent    = "smart-proxy/component"
	LabelInstance     = "smart-proxy/instance" // Tells apart several Smart Proxy installations in one namespace
	ComponentRoutes   = "routes"
	ComponentActivity = "activity"
)

// ConfigMapBackend keeps routes in a ConfigMap shared by every Smart Proxy replica.
type ConfigMapBackend struct {
	client    kubernetes.Interface
	namespace string
	name      string
	// owner, when set, makes Kubernetes delete the ConfigMap together with that object
	// (the Smart Proxy Deployment), so uninstalling leaves nothing behind.
	owner    *metav1.OwnerReference
	instance string
}

func NewConfigMapBackend(client kubernetes.Interface, namespace, name, instance string, owner *metav1.OwnerReference) *ConfigMapBackend {
	return &ConfigMapBackend{client: client, namespace: namespace, name: name, instance: instance, owner: owner}
}

// Name returns the ConfigMap's name.
func (b *ConfigMapBackend) Name() string { return b.name }

// Exists reports whether the ConfigMap has been created yet.
func (b *ConfigMapBackend) Exists() (bool, error) {
	_, err := b.client.CoreV1().ConfigMaps(b.namespace).Get(context.TODO(), b.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (b *ConfigMapBackend) Load() ([]*RouteConfig, error) {
	cm, err := b.client.CoreV1().ConfigMaps(b.namespace).Get(context.TODO(), b.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return DecodeRoutes([]byte(cm.Data[ConfigMapKey]))
}

func (b *ConfigMapBackend) Update(mutate func(map[string]*RouteConfig)) ([]*RouteConfig, error) {
	var result []*RouteConfig
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cms := b.client.CoreV1().ConfigMaps(b.namespace)
		cm, err := cms.Get(context.TODO(), b.name, metav1.GetOptions{})
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if create {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:      b.name,
				Namespace: b.namespace,
				Labels:    map[string]string{LabelComponent: ComponentRoutes, LabelInstance: b.instance},
			}}
			if b.owner != nil {
				cm.OwnerReferences = []metav1.OwnerReference{*b.owner}
			}
		}

		routes, err := DecodeRoutes([]byte(cm.Data[ConfigMapKey]))
		if err != nil {
			return err
		}
		m := toMap(routes)
		mutate(m)
		data, err := EncodeRoutes(m, false)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[ConfigMapKey] = string(data)

		if create {
			_, err = cms.Create(context.TODO(), cm, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(corev1.Resource("configmaps"), b.name, err) // Created meanwhile: retry
			}
		} else {
			_, err = cms.Update(context.TODO(), cm, metav1.UpdateOptions{})
		}
		if err == nil {
			result = toList(m)
		}
		return err
	})
	return result, err
}
