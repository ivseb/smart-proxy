// Package restore undoes everything Smart Proxy changed in the cluster, so it can be
// uninstalled without breaking applications: patched Ingresses and Routes get their original
// backend back, and sleeping Deployments are woken up.
package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
)

// Result counts what Restore changed.
type Result struct {
	Ingresses   int
	Routes      int
	Deployments int
	Errors      []error
}

// Run unpatches every Ingress and Route in the watched namespaces and wakes every Deployment
// Smart Proxy put to sleep. It keeps going after a failure and reports all of them.
func Run(c *k8s.Client) Result {
	var res Result
	fail := func(format string, args ...any) {
		err := fmt.Errorf(format, args...)
		logger.Printf("Restore: %v", err)
		res.Errors = append(res.Errors, err)
	}

	ings, err := c.ListIngresses()
	if err != nil {
		fail("listing ingresses: %w", err)
	}
	for _, ing := range ings {
		if ing.Annotations[k8s.AnnotationPatched] != "true" {
			continue
		}
		if err := k8s.UnpatchIngress(ing); err != nil {
			fail("unpatching Ingress %s/%s: %w", ing.Namespace, ing.Name, err)
			continue
		}
		if err := c.UpdateIngress(ing); err != nil {
			fail("updating Ingress %s/%s: %w", ing.Namespace, ing.Name, err)
			continue
		}
		logger.Printf("Restore: Ingress %s/%s points at its original backend again", ing.Namespace, ing.Name)
		res.Ingresses++
	}

	routes, err := c.ListRoutes()
	if err != nil {
		fail("listing routes: %w", err)
	}
	for _, rt := range routes {
		if rt.Annotations[k8s.AnnotationPatched] != "true" {
			continue
		}
		if err := k8s.UnpatchRoute(rt); err != nil {
			fail("unpatching Route %s/%s: %w", rt.Namespace, rt.Name, err)
			continue
		}
		if err := c.UpdateRoute(rt); err != nil {
			fail("updating Route %s/%s: %w", rt.Namespace, rt.Name, err)
			continue
		}
		logger.Printf("Restore: Route %s/%s points at its original backend again", rt.Namespace, rt.Name)
		res.Routes++
	}

	sleeping, err := c.SleepingDeployments()
	if err != nil {
		fail("listing deployments: %w", err)
	}
	for _, d := range sleeping {
		replicas, err := c.WakeDeployment(d.Namespace, d.Ref)
		if err != nil {
			fail("waking %s/%s: %w", d.Namespace, d.Ref, err)
			continue
		}
		logger.Printf("Restore: woke %s/%s with %d replica(s)", d.Namespace, d.Ref, replicas)
		res.Deployments++
	}

	logger.Printf("Restore: %d Ingress(es) and %d Route(s) unpatched, %d Deployment(s) woken, %d error(s)",
		res.Ingresses, res.Routes, res.Deployments, len(res.Errors))
	return res
}

// Err returns the failures as one error, or nil.
func (r Result) Err() error {
	return errors.Join(r.Errors...)
}

// StopProxy scales the Smart Proxy Deployment to zero and waits for its pods to receive
// SIGTERM, which stops their watcher (and its self-healing) immediately.
func StopProxy(ctx context.Context, c *k8s.Client, namespace, name string) error {
	patch := []byte(`{"spec":{"replicas":0}}`)
	_, err := c.Clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	logger.Printf("Restore: scaled %s/%s to 0, waiting for it to stop", namespace, name)

	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance"})
		if err != nil {
			return err
		}
		running := 0
		for _, p := range pods.Items {
			if p.DeletionTimestamp == nil && strings.HasPrefix(p.Name, name+"-") && p.Labels["app.kubernetes.io/component"] != "restore" {
				running++
			}
		}
		if running == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("pods of %s/%s still running after a minute", namespace, name)
}
