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
	configPath := getEnv("CONFIG_PATH", "routes.json")
	configStore := store.NewStore(configPath)

	// Name of the Service fronting Smart Proxy: patched Ingresses/Routes are pointed at it.
	// Helm sets this to the release fullname, which is not always "smart-proxy".
	serviceName := getEnv("SMART_PROXY_SERVICE_NAME", "smart-proxy")
	proxyAddr := ":" + getEnv("SMART_PROXY_PORT", "8080")

	// 3. Initialize Proxy Handler
	proxyHandler := proxy.NewHandler(k8sClient, configStore)

	// 4. Initialize Watcher (Auto-scaler)
	watcherService := watcher.NewWatcher(k8sClient, configStore, serviceName)
	go watcherService.Start()

	// 5. Start Admin Server (Port 8081)
	go func() {
		log.Println("Admin Server listening on :8081")
		adminServer := admin.NewServer(k8sClient, configStore, proxyHandler.Metrics, serviceName)
		if err := adminServer.ListenAndServe(":8081"); err != nil {
			log.Printf("Admin Server failed: %v", err)
		}
	}()

	// 6. Start Proxy Server
	log.Printf("Proxy Server listening on %s", proxyAddr)
	if err := http.ListenAndServe(proxyAddr, proxyHandler); err != nil {
		log.Fatalf("Proxy Server failed: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
