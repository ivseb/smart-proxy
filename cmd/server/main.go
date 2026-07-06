package main

import (
	"log"
	"net/http"
	"os"

	"smart-proxy/internal/admin"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/store"
	"smart-proxy/internal/watcher"
	// "smart-proxy/internal/watcher"
)

func main() {
	log.Println("Starting OpenShift Smart Proxy...")

	// 1. Initialize K8s Client
	k8sClient, err := k8s.NewClient()
	if err != nil {
		log.Printf("Warning: Failed to initialize Kubernetes client: %v", err)
		log.Println("Running in offline/demo mode (K8s features disabled)")
		// In a real app we might want to exit, but for dev we might want to continue
	}

	// 2. Initialize Config Store
	// Use environment variable for config path or default
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "routes.json"
	}
	configStore := store.NewStore(configPath)

	// 3. Initialize Proxy Handler
	proxyHandler := proxy.NewHandler(k8sClient, configStore)

	// 4. Initialize Watcher (Auto-scaler)
	watcherService := watcher.NewWatcher(k8sClient, configStore)
	go watcherService.Start()

	// 5. Start Admin Server (Port 8081)
	// 5. Start Admin Server (Port 8081)
	go func() {
		log.Println("Admin Server listening on :8081")
		adminServer := admin.NewServer(k8sClient, configStore, proxyHandler.Metrics)
		if err := adminServer.ListenAndServe(":8081"); err != nil {
			log.Printf("Admin Server failed: %v", err)
		}
	}()

	// 6. Start Proxy Server (Port 8080)
	log.Println("Proxy Server listening on :8080")
	if err := http.ListenAndServe(":8080", proxyHandler); err != nil {
		log.Fatalf("Proxy Server failed: %v", err)
	}
}
