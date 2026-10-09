package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smart-proxy/internal/admin"
	"smart-proxy/internal/auth"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/restore"
	"smart-proxy/internal/store"
	"smart-proxy/internal/watcher"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		runRestore()
		return
	}

	log.Println("Starting OpenShift Smart Proxy...")

	// Stop on SIGTERM (Kubernetes) or Ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// 1. Initialize K8s Client for the watched namespaces
	k8sClient, err := newK8sClient()
	if err != nil {
		log.Printf("Warning: Failed to initialize Kubernetes client: %v", err)
		log.Println("Running in offline/demo mode (K8s features disabled)")
		k8sClient = nil
	}

	// 2. Initialize Config Store
	// Use environment variable for config path or default
	configPath := getEnv("CONFIG_PATH", "routes.json")
	configStore := store.NewStore(configPath)

	// Name of the Service fronting Smart Proxy: patched Ingresses/Routes are pointed at it.
	// Helm sets this to the release fullname, which is not always "smart-proxy".
	serviceName := getEnv("SMART_PROXY_SERVICE_NAME", "smart-proxy")
	proxyAddr := ":" + getEnv("SMART_PROXY_PORT", "8080")

	// Admin dashboard authentication (AUTH_MODE and AUTH_* variables).
	authConfig, err := auth.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("Invalid authentication configuration: %v", err)
	}
	authn, err := auth.New(authConfig)
	if err != nil {
		log.Fatalf("Failed to initialize authentication: %v", err)
	}
	// Set to 127.0.0.1:8081 when an auth proxy sidecar fronts the dashboard, so it can't be bypassed.
	adminAddr := getEnv("ADMIN_ADDR", ":8081")

	// Time between failing the readiness probe and closing listeners on shutdown, so
	// Kubernetes stops routing new requests here first.
	shutdownDelay, err := time.ParseDuration(getEnv("SHUTDOWN_DELAY", "5s"))
	if err != nil {
		log.Fatalf("Invalid SHUTDOWN_DELAY: %v", err)
	}

	// 3. Initialize Proxy Handler
	proxyHandler := proxy.NewHandler(k8sClient, configStore)

	// 4. Admin Server
	adminServer := admin.NewServer(k8sClient, configStore, proxyHandler.Metrics, serviceName, authn)
	// Cancelled at shutdown so long-lived requests (the log stream) end instead of
	// holding the server open until the deadline.
	adminCtx, cancelAdmin := context.WithCancel(context.Background())
	adminHTTP := &http.Server{
		Addr:              adminAddr,
		Handler:           adminServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return adminCtx },
	}

	// 6. Proxy Server
	proxyHTTP := &http.Server{
		Addr:              proxyAddr,
		Handler:           proxyHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 2)
	for name, srv := range map[string]*http.Server{"Admin": adminHTTP, "Proxy": proxyHTTP} {
		go func(name string, srv *http.Server) {
			log.Printf("%s Server listening on %s", name, srv.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- errors.New(name + " Server failed: " + err.Error())
			}
		}(name, srv)
	}

	// 5. Fill the Kubernetes caches, then start serving and watching. The listeners are
	// already up so liveness passes meanwhile; readiness waits for this.
	if k8sClient != nil {
		if err := k8sClient.Start(ctx, 2*time.Minute); err != nil {
			log.Fatalf("Failed to start Kubernetes caches: %v", err)
		}
		adminServer.SyncRoutesFromCluster()
	}
	proxyHandler.SetReady()
	go watcher.NewWatcher(k8sClient, configStore, serviceName).Start(ctx)

	select {
	case err := <-errs:
		log.Fatal(err)
	case <-ctx.Done():
	}
	stop() // A second signal terminates immediately.

	log.Printf("Shutting down: draining for %s...", shutdownDelay)
	proxyHandler.SetDraining()
	time.Sleep(shutdownDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cancelAdmin()
	if err := proxyHTTP.Shutdown(shutdownCtx); err != nil {
		log.Printf("Proxy Server shutdown: %v", err)
	}
	if err := adminHTTP.Shutdown(shutdownCtx); err != nil {
		log.Printf("Admin Server shutdown: %v", err)
	}
	log.Println("Stopped")
}

// newK8sClient connects to the cluster for the namespaces in WATCH_NAMESPACE(_SELECTOR).
func newK8sClient() (*k8s.Client, error) {
	ownNamespace := k8s.OwnNamespace()
	scope, err := k8s.ParseScope(os.Getenv("WATCH_NAMESPACE"), os.Getenv("WATCH_NAMESPACE_SELECTOR"), ownNamespace)
	if err != nil {
		log.Fatalf("Invalid namespace configuration: %v", err)
	}
	return k8s.NewClient(scope, ownNamespace)
}

// runRestore implements "smart-proxy restore": undo every patch and wake every sleeping
// Deployment in the watched namespaces. The Helm chart runs it before uninstalling.
func runRestore() {
	log.Println("Restoring patched Ingresses/Routes and sleeping Deployments...")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	client, err := newK8sClient()
	if err != nil {
		log.Fatalf("Failed to connect to Kubernetes: %v", err)
	}
	if err := client.Start(ctx, 2*time.Minute); err != nil {
		log.Fatalf("Failed to start Kubernetes caches: %v", err)
	}
	// Stop the running Smart Proxy first, or its self-healing could re-patch what we restore.
	if name := os.Getenv("SMART_PROXY_DEPLOYMENT"); name != "" {
		if err := restore.StopProxy(ctx, client, k8s.OwnNamespace(), name); err != nil {
			log.Fatalf("Failed to stop Smart Proxy before restoring: %v", err)
		}
	}
	if err := restore.Run(client).Err(); err != nil {
		log.Fatalf("Restore finished with errors:\n%v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
