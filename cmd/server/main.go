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
	_ "time/tzdata" // Schedules use IANA timezones; the runtime image has no zoneinfo

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/admin"
	"smart-proxy/internal/auth"
	"smart-proxy/internal/ha"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/metrics"
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

	// Name of the Service fronting Smart Proxy: patched Ingresses/Routes are pointed at it.
	// Helm sets this to the release fullname, which is not always "smart-proxy".
	serviceName := getEnv("SMART_PROXY_SERVICE_NAME", "smart-proxy")
	podName := getEnv("POD_NAME", hostname())

	// 2. Initialize Config Store: a ConfigMap shared by all replicas in a cluster, a file offline.
	configPath := getEnv("CONFIG_PATH", "routes.json")
	var configStore *store.Store
	if k8sClient != nil {
		configStore = newSharedStore(k8sClient, serviceName, configPath)
	} else {
		configStore = store.NewStore(configPath)
	}
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
	adminServer.Replica = podName
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

	// Prometheus metrics, on their own port: never exposed through the proxy or behind the dashboard login.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsHTTP := &http.Server{Addr: getEnv("METRICS_ADDR", ":9090"), Handler: metricsMux, ReadHeaderTimeout: 10 * time.Second}

	errs := make(chan error, 3)
	for name, srv := range map[string]*http.Server{"Admin": adminHTTP, "Proxy": proxyHTTP, "Metrics": metricsHTTP} {
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
		replica := &ha.Replica{
			Client:    k8sClient.Clientset,
			Namespace: k8s.OwnNamespace(),
			Instance:  serviceName,
			PodName:   podName,
			PodUID:    os.Getenv("POD_UID"),
			Store:     configStore,
			LocalRequests: func() ha.RequestCounts {
				total, routes := proxyHandler.Metrics.Snapshot()
				return ha.RequestCounts{Total: total, Routes: routes}
			},
		}
		if err := replica.Start(ctx); err != nil {
			log.Fatalf("Failed to watch shared configuration: %v", err)
		}
		adminServer.RequestTotals = func() (int64, map[string]int64) {
			c := replica.ClusterRequests()
			return c.Total, c.Routes
		}
		adminServer.SyncRoutesFromCluster()
		proxyHandler.SetReady()

		// Only the leader puts deployments to sleep and heals patches; every replica proxies and wakes.
		w := watcher.NewWatcher(k8sClient, configStore, serviceName)
		go replica.RunLeaderElection(ctx, func(leadCtx context.Context) {
			metrics.SetLeader(true)
			defer metrics.SetLeader(false)
			w.Start(leadCtx)
		})
		metrics.RegisterSleeping(func() (map[string]int, map[string]int) {
			sleeping, _ := k8sClient.SleepingDeployments()
			namespaces, recorded := make([]string, len(sleeping)), make([]string, len(sleeping))
			for i, d := range sleeping {
				namespaces[i], recorded[i] = d.Namespace, d.Recorded
			}
			return metrics.SleepingFromAnnotations(namespaces, recorded)
		})
	} else {
		proxyHandler.SetReady()
	}

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
	metricsHTTP.Shutdown(shutdownCtx)
	log.Println("Stopped")
}

// newSharedStore keeps routes in a ConfigMap in Smart Proxy's namespace, shared by all replicas.
// On the first start, routes from an older file-based installation (CONFIG_PATH) are imported.
func newSharedStore(client *k8s.Client, instance, legacyFile string) *store.Store {
	ns := k8s.OwnNamespace()
	var owner *metav1.OwnerReference
	if name := os.Getenv("SMART_PROXY_DEPLOYMENT"); name != "" {
		// Owned by the Smart Proxy Deployment: deleted with it when uninstalled.
		if dep, err := client.Clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{}); err == nil {
			owner = &metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID}
		} else {
			log.Printf("Warning: cannot read Deployment %s/%s (%v); the routes ConfigMap won't be removed on uninstall", ns, name, err)
		}
	}
	backend := store.NewConfigMapBackend(client.Clientset, ns, ha.RoutesConfigMap(instance), instance, owner)

	if exists, err := backend.Exists(); err != nil {
		log.Fatalf("Cannot read ConfigMap %s/%s: %v", ns, backend.Name(), err)
	} else if !exists {
		if legacy, err := store.NewFileBackend(legacyFile).Load(); err == nil && len(legacy) > 0 {
			if _, err := backend.Update(func(m map[string]*store.RouteConfig) {
				for _, r := range legacy {
					if r.ID == "" {
						r.ID = uuid.New().String() // Very old files had no IDs
					}
					m[r.ID] = r
				}
			}); err != nil {
				log.Fatalf("Failed to import %s into ConfigMap %s: %v", legacyFile, backend.Name(), err)
			}
			log.Printf("Imported %d route(s) from %s into ConfigMap %s/%s", len(legacy), legacyFile, ns, backend.Name())
		}
	}
	return store.NewStoreWithBackend(backend)
}

func hostname() string {
	name, _ := os.Hostname()
	return name
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
		// Not owned by anything Helm deletes, unlike the ConfigMaps.
		lease := getEnv("SMART_PROXY_SERVICE_NAME", "smart-proxy") + "-leader"
		if err := client.Clientset.CoordinationV1().Leases(k8s.OwnNamespace()).Delete(ctx, lease, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.Printf("Warning: could not delete Lease %s: %v", lease, err)
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
