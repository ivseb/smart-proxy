package watcher

import (
	"time"

	"smart-proxy/internal/logger"
)

// A GitOps tool with self-healing (Argo CD, Flux) reverts what Smart Proxy changes: patched
// Ingresses/Routes go back to the application, scaled-down workloads back to their replicas.
// Fighting it every 30 seconds churns the cluster and the application for nothing, so after a
// few rounds Smart Proxy steps back for a while and says why.
const (
	fightWindow  = 10 * time.Minute
	fightRounds  = 3
	fightPause   = time.Hour
	gitOpsAdvice = "if a GitOps tool manages it, enable Smart Proxy declaratively (smart-proxy/enabled annotation in Git) or have the tool ignore the fields Smart Proxy changes"
)

// sleepSettle is how long after a sleep a workload found running isn't held against anyone:
// the caches may not show the sleep yet.
var sleepSettle = time.Minute

type sleepRecord struct {
	at        time.Time   // Our latest successful sleep
	counted   bool        // Found running again since, without Smart Proxy waking it (counted once)
	externals []time.Time // Such scale-ups, within the fight window
}

func (w *Watcher) isPaused(key string) bool {
	until, ok := w.paused[key]
	if ok && time.Now().After(until) {
		delete(w.paused, key)
		return false
	}
	return ok
}

// mayHeal records a re-patch of a resource; it refuses when the resource keeps being reverted.
func (w *Watcher) mayHeal(resource string) bool {
	if w.isPaused(resource) {
		return false
	}
	now := time.Now()
	recent := w.heals[resource][:0]
	for _, t := range w.heals[resource] {
		if now.Sub(t) < fightWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= fightRounds {
		delete(w.heals, resource)
		w.paused[resource] = now.Add(fightPause)
		logger.Printf("Self-Healing: %s was reverted %d times in %s: something keeps undoing the patch. Leaving it alone for %s; %s.",
			resource, len(recent), fightWindow, fightPause, gitOpsAdvice)
		return false
	}
	w.heals[resource] = append(recent, now)
	return true
}

// maySleep refuses to put a workload to sleep when something else keeps scaling it back up.
func (w *Watcher) maySleep(namespace, workload, key string) bool {
	if w.isPaused(key) {
		return false
	}
	last, ok := w.sleeps[key]
	if replicas, _, err := w.k8sClient.GetDeploymentStatus(namespace, workload); !ok || err != nil || replicas == 0 {
		return true // Never slept here, or still asleep: nothing to tell
	}
	// Slept moments ago (the cache may not show it yet, e.g. a workload shared by several
	// routes), already counted for this sleep, or woken by Smart Proxy since: the normal cycle.
	if time.Since(last.at) < sleepSettle || last.counted {
		return true
	}
	if woken, ok := w.k8sClient.WokenAt(namespace, workload); ok && !woken.Before(last.at.Truncate(time.Second)) {
		return true
	}
	now := time.Now()
	last.counted = true
	recent := last.externals[:0]
	for _, t := range last.externals {
		if now.Sub(t) < fightWindow {
			recent = append(recent, t)
		}
	}
	last.externals = append(recent, now)
	w.sleeps[key] = last
	if len(last.externals) >= fightRounds {
		delete(w.sleeps, key)
		w.paused[key] = now.Add(fightPause)
		logger.Printf("Not putting %s to sleep for %s: it was scaled back up %d times in %s without a request waking it; %s.",
			key, fightPause, len(last.externals), fightWindow, gitOpsAdvice)
		return false
	}
	return true
}
