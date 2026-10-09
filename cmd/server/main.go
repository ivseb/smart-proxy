package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // Schedules use IANA timezones; the runtime image has no zoneinfo

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/admin"
	"smart-proxy/internal/auth"
	"smart-proxy/internal/guard"
	"smart-proxy/internal/ha"
	"smart-proxy/internal/history"
	"smart-proxy/internal/inspect"
	"smart-proxy/internal/k8s"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/proxy"
	"smart-proxy/internal/restore"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
	"smart-proxy/internal/vault"
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
	globalRules, trusted := trafficConfig()
	proxyHandler.GlobalRules, proxyHandler.TrustedProxies = globalRules, trusted
	proxyHandler.Traffic = traffic.NewRecorder()
	// How long API calls and other non-page requests wait for a sleeping app.
	if proxyHandler.WakeTimeout, err = time.ParseDuration(getEnv("WAKE_TIMEOUT", "2m")); err != nil {
		log.Fatalf("Invalid WAKE_TIMEOUT: %v", err)
	}
	proxyHandler.ClusterDomain = getEnv("CLUSTER_DOMAIN", "cluster.local")

	// 4. Admin Server
	adminServer := admin.NewServer(k8sClient, configStore, proxyHandler.Metrics, serviceName, authn)
	adminServer.Replica = podName
	adminServer.GlobalRules = globalRules
	adminServer.Traffic = func(routeID string) []traffic.SourceStats {
		return traffic.Merge(proxyHandler.Traffic.Snapshot()[routeID])
	}
	// Recent request rates for the dashboard's charts, kept in memory.
	retention, err := time.ParseDuration(getEnv("STATS_RETENTION", "30m"))
	if err != nil || retention <= 0 {
		log.Fatalf("Invalid STATS_RETENTION %q", os.Getenv("STATS_RETENTION"))
	}
	requestHistory := &history.Recorder{
		Interval:  10 * time.Second,
		Retention: retention,
		Sample: func() map[string]history.Counts {
			total, routes := proxyHandler.Metrics.Snapshot()
			return map[string]history.Counts{podName: {Total: total, Routes: routes}}
		},
	}
	adminServer.History = requestHistory
	// Requests of inspected routes, recorded by each replica and gathered by the dashboard.
	recorder := &inspect.Recorder{Replica: podName}
	proxyHandler.Inspect, adminServer.Inspect = recorder, recorder
	var secrets atomic.Pointer[vault.Vault] // Set once the cluster connection is up
	adminServer.Vault = secrets.Load
	adminServer.PeerToken = func() string { return secrets.Load().PeerToken() }
	if k8sClient != nil {
		metricsPort := getEnv("METRICS_ADDR", ":9090")
		ownIP := os.Getenv("POD_IP")
		adminServer.Peers = func() []string {
			var peers []string
			for _, ip := range k8sClient.ProxyPodIPs() {
				if ip != ownIP {
					peers = append(peers, "http://"+net.JoinHostPort(ip, strings.TrimPrefix(metricsPort, ":")))
				}
			}
			return peers
		}
	}
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
		// Longer than the keep-alive of ingress controllers and routers towards their backends,
		// so they close idle connections first (no request lost on a connection closed here).
		IdleTimeout: 10 * time.Minute,
		// HTTP/2 without TLS too (h2c), as ingress controllers send gRPC.
		Protocols: func() *http.Protocols {
			var p http.Protocols
			p.SetHTTP1(true)
			p.SetUnencryptedHTTP2(true)
			return &p
		}(),
	}

	// Prometheus metrics, on their own port: never exposed through the proxy or behind the dashboard login.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsMux.Handle(inspect.PeerPath, recorder.PeerHandler(func() string { return secrets.Load().PeerToken() }))
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
		// Patched resources outside Smart Proxy's namespace reach it through stand-in Services.
		k8sClient.EnableStandIns(ctx, serviceName)
		// Smart Proxy's own Secret: peer token, login key, route credentials.
		v := &vault.Vault{Client: k8sClient.Clientset, Namespace: k8s.OwnNamespace(), Name: serviceName + "-state", Owner: ownDeployment(k8sClient)}
		if err := v.Start(ctx); err != nil {
			log.Printf("Warning: Smart Proxy's Secret is unavailable (%v): the inspector shows this replica only, and routes can't be protected", err)
		} else {
			secrets.Store(v)
		}
		if v := secrets.Load(); v != nil {
			proxyHandler.Guard = &guard.Guard{Vault: v} // Read by requests only once ready (below)
		}
		replica := &ha.Replica{
			Client:       k8sClient.Clientset,
			Namespace:    k8s.OwnNamespace(),
			Instance:     serviceName,
			PodName:      podName,
			PodUID:       os.Getenv("POD_UID"),
			Store:        configStore,
			LocalTraffic: proxyHandler.Traffic.Snapshot,
			LocalRequests: func() ha.RequestCounts {
				total, routes := proxyHandler.Metrics.Snapshot()
				return ha.RequestCounts{Total: total, Routes: routes}
			},
		}
		if err := replica.Start(ctx); err != nil {
			log.Fatalf("Failed to watch shared configuration: %v", err)
		}
		adminServer.Traffic = replica.ClusterTraffic
		adminServer.RequestTotals = func() (int64, map[string]int64) {
			c := replica.ClusterRequests()
			return c.Total, c.Routes
		}
		requestHistory.Sample = func() map[string]history.Counts {
			all := map[string]history.Counts{}
			for pod, c := range replica.RequestsByReplica() {
				all[pod] = history.Counts{Total: c.Total, Routes: c.Routes}
			}
			return all
		}
		adminServer.SyncRoutesFromCluster()
		proxyHandler.SetReady()

		// Only the leader puts deployments to sleep and heals patches; every replica proxies and wakes.
		w := watcher.NewWatcher(k8sClient, configStore, serviceName)
		go replica.RunLeaderElection(ctx, func(leadCtx context.Context) {
			metrics.SetLeader(true)
			defer metrics.SetLeader(false)
			k8sClient.SetLeading(true)
			defer k8sClient.SetLeading(false)
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

	go requestHistory.Run(ctx)
	// Recorded requests hold header values: forget them a while after recording stops, and at
	// once for deleted routes.
	go func() {
		for range time.Tick(time.Minute) {
			for _, id := range recorder.Routes() {
				route, ok := configStore.GetRoute(id)
				if !ok || route.InspectUntil == nil || time.Since(*route.InspectUntil) > 15*time.Minute {
					recorder.Clear(id)
				}
			}
		}
	}()

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
	backend := store.NewConfigMapBackend(client.Clientset, ns, ha.RoutesConfigMap(instance), instance, ownDeployment(client))

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

// ownDeployment references Smart Proxy's own Deployment (SMART_PROXY_DEPLOYMENT), so what it
// creates for itself (routes ConfigMap, Secret) is deleted with it when uninstalled.
func ownDeployment(client *k8s.Client) *metav1.OwnerReference {
	name := os.Getenv("SMART_PROXY_DEPLOYMENT")
	if name == "" {
		return nil
	}
	ns := k8s.OwnNamespace()
	dep, err := client.Clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		log.Printf("Warning: cannot read Deployment %s/%s (%v); what Smart Proxy creates for itself won't be removed on uninstall", ns, name, err)
		return nil
	}
	return &metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID}
}

// trafficConfig reads the global rules for requests that never count as activity, and the
// proxies trusted for X-Forwarded-For. IGNORE_USER_AGENTS adds to the built-in list of
// monitors (IGNORE_DEFAULT_USER_AGENTS=false drops it); TRUSTED_PROXIES=none trusts no proxy.
func trafficConfig() (traffic.Rules, traffic.TrustedProxies) {
	list := func(key string, defaults []string) []string {
		v := strings.TrimSpace(os.Getenv(key))
		switch {
		case v == "":
			return defaults
		case strings.EqualFold(v, "none"):
			return nil
		default:
			return traffic.SplitList(v)
		}
	}
	var userAgents []string
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("IGNORE_DEFAULT_USER_AGENTS")), "false") {
		userAgents = append(userAgents, traffic.DefaultUserAgents...)
	}
	userAgents = append(userAgents, list("IGNORE_USER_AGENTS", nil)...)
	rules := traffic.Rules{
		UserAgents: userAgents,
		Paths:      list("IGNORE_PATHS", nil),
		Sources:    list("IGNORE_SOURCES", nil),
		Methods:    list("IGNORE_METHODS", nil),
	}
	if err := rules.Validate(); err != nil {
		log.Fatalf("Invalid IGNORE_* settings: %v", err)
	}
	trusted, err := traffic.ParseTrustedProxies(list("TRUSTED_PROXIES", traffic.DefaultTrustedProxies))
	if err != nil {
		log.Fatalf("Invalid TRUSTED_PROXIES: %v", err)
	}
	return rules, trusted
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
	if err := restore.Run(client, getEnv("SMART_PROXY_SERVICE_NAME", "smart-proxy")).Err(); err != nil {
		log.Fatalf("Restore finished with errors:\n%v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
