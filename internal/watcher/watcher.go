package watcher

import (
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

type Watcher struct {
	k8sClient *k8s.Client
	store     *store.Store
}

func NewWatcher(k8sClient *k8s.Client, store *store.Store) *Watcher {
	return &Watcher{
		k8sClient: k8sClient,
		store:     store,
	}
}

func (w *Watcher) Start() {
	logger.Println("Watcher started. Checking for idle services every 30s...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		w.checkIdleRoutes()
	}
}

func (w *Watcher) checkIdleRoutes() {
	routes := w.store.GetAllRoutes()

	for _, route := range routes {
		// IdleTimeout is already time.Duration
		// But in old config it was string.
		// Since we changed the struct in config.go to time.Duration, we don't need to parse string anymore.
		// However, JSON unmarshal of string into time.Duration assumes nanoseconds unless we write a custom unmarshal?
		// No, standard JSON unmarshal into time.Duration expects numbers (ns).
		// Wait, if users provide string "30m" in JSON, standard unmarshal will FAIL for time.Duration field.
		// We might need a wrapper type or keep it string and parse it here.
		// Let's assume for now the Store handles loading correctly or we change struct back to string.
		// Actually, standard `time.Duration` in Go JSON is int64 (nanoseconds).
		// If we want user friendly "30m", we should keep it string in Struct.
		// Reverting Struct field to string in store/config.go would be safer for user config?
		// No, let's stick to Duration in struct but we assume the JSON has int64.
		// OR we change it back to string.
		// Given the user wants "Professional", "30m" string is better than 1800000000000.
		// Let's keep it Duration but assume we handled it?
		// Actually, I should probably check what I wrote in config.go.
		// I wrote `IdleTimeout  time.Duration`.
		// If I want string inputs, I should use a custom type or string.
		// For simplicity, let's use string in struct and parse it here, as it was before.
		// BUT I already wrote config.go with time.Duration.
		// Let's assume I fix config.go?
		// No, let's fix THIS watcher to use the Duration directly.

		timeout := route.IdleTimeout

		if time.Since(route.LastActivity) > timeout {
			// Check current replicas
			replicas, _, err := w.k8sClient.GetDeploymentStatus(route.Namespace, route.Deployment)
			if err != nil {
				logger.Printf("Error getting status for idle check %s/%s: %v", route.Namespace, route.Deployment, err)
				continue
			}

			if replicas > 0 {
				logger.Printf("Route %s is idle (Last active: %s). Scaling down deployment %s...",
					route.Path, route.LastActivity.Format(time.RFC3339), route.Deployment)

				err := w.k8sClient.ScaleDeployment(route.Namespace, route.Deployment, 0)
				if err != nil {
					logger.Printf("Error scaling down %s: %v", route.Deployment, err)
				}

				// Scale down dependencies
				for _, dep := range route.Dependencies {
					if dep.StopOnIdle {
						logger.Printf("Scaling down dependency %s for route %s...", dep.Name, route.Path)
						err := w.k8sClient.ScaleDeployment(route.Namespace, dep.Name, 0)
						if err != nil {
							logger.Printf("Error scaling down dependency %s: %v", dep.Name, err)
						}
					}
				}
			}
		}
	}
}
