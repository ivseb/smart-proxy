package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReleaseNamespace undoes what Smart Proxy did for a route in a namespace it no longer watches
// (removed from the list, or its label no longer matches the selector): the Ingresses/Routes
// patched for it point at the application again, and the workloads it put to sleep are woken.
// Otherwise nothing would wake them, and the patched resources would lead nowhere.
//
// It uses the API directly (the caches only cover watched namespaces), so it works as long as
// the RBAC still allows it. owns tells whether a patched resource belongs to the route, from
// its smart-proxy/config annotation.
func (c *Client) ReleaseNamespace(namespace, proxyService string, owns func(config RouteOwner) bool, workloads []string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	released := 0
	var errs []error

	ings, err := c.Clientset.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		errs = append(errs, err)
	default:
		for i := range ings.Items {
			ing := &ings.Items[i]
			if !owns(ownerOf("Ingress", ing.Name, ing.Annotations)) || PatchedByOther(ing, proxyService) {
				continue
			}
			if err := UnpatchIngress(ing); err == nil {
				if _, err := c.Clientset.NetworkingV1().Ingresses(namespace).Update(ctx, ing, metav1.UpdateOptions{}); err != nil {
					errs = append(errs, err)
					continue
				}
				released++
			}
		}
	}

	if c.RoutesEnabled() {
		routes, err := c.RouteClientSet.RouteV1().Routes(namespace).List(ctx, metav1.ListOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			errs = append(errs, err)
		default:
			for i := range routes.Items {
				rt := &routes.Items[i]
				if !owns(ownerOf("Route", rt.Name, rt.Annotations)) || RoutePatchedByOther(rt, proxyService) {
					continue
				}
				if err := UnpatchRoute(rt); err == nil {
					if _, err := c.RouteClientSet.RouteV1().Routes(namespace).Update(ctx, rt, metav1.UpdateOptions{}); err != nil {
						errs = append(errs, err)
						continue
					}
					released++
				}
			}
		}
	}

	for _, ref := range workloads {
		w, err := c.fetchWorkload(namespace, ref)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, slept := w.Annotations[AnnotationReplicasBeforeSleep]; !slept || w.replicas() > 0 {
			continue // Not put to sleep by Smart Proxy
		}
		if _, err := c.wake(namespace, ref); err != nil {
			errs = append(errs, err)
		} else {
			released++
		}
	}
	if err := c.deleteStandIn(namespace, proxyService); err != nil {
		errs = append(errs, err)
	}
	return released, errors.Join(errs...)
}

// RouteOwner identifies the route a patched resource belongs to.
type RouteOwner struct {
	Patched bool
	ID      string // From the smart-proxy/config annotation ("" for old patches)
	Kind    string // The resource itself
	Name    string
}

func ownerOf(kind, name string, annotations map[string]string) RouteOwner {
	owner := RouteOwner{Patched: annotations[AnnotationPatched] == "true", Kind: kind, Name: name}
	var config struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(annotations[AnnotationConfig]), &config) == nil {
		owner.ID = config.ID
	}
	return owner
}
