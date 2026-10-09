//go:build e2e

// Package e2e tests Smart Proxy in a real cluster: see run.sh, which sets it up.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// Requests go through ingress-nginx and the patched Ingress, as users' do.
	proxyURL = "http://localhost:30090"
	// Smart Proxy's own Service, for what ingress-nginx can't pass as-is (h2c).
	directURL = "http://localhost:30080"
	adminURL  = "http://localhost:30081"
	service   = "e2e-smart-proxy"
	appHost   = "echo.e2e.test"
	appNS     = "e2e"
	proxyNS   = "smart-proxy"
	release   = "e2e"
	routeID   = "ing-e2e/echo"
	idleLimit = 60 * time.Second
)

var kube kubernetes.Interface

func TestE2E(t *testing.T) {
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	kube = kubernetes.NewForConfigOrDie(config)

	eventually(t, time.Minute, "dashboard not reachable", func() bool {
		resp, err := admin("GET", "/api/info", nil)
		return err == nil && resp.StatusCode == 200
	})
	if resp, err := admin("POST", "/api/patch-ingress?namespace=e2e&name=echo", nil); err != nil || (resp.StatusCode != 200 && resp.StatusCode != 400) {
		t.Fatalf("patching the Ingress: %v %v", resp, err)
	}
	setIdleTimeout(t, idleLimit)
	// The Ingress is in another namespace than Smart Proxy: it reaches it through a stand-in.
	eventually(t, 30*time.Second, "no stand-in Service in the app's namespace", func() bool {
		ep, err := kube.CoreV1().Endpoints(appNS).Get(context.TODO(), service, metav1.GetOptions{})
		return err == nil && len(ep.Subsets) == 1 && len(ep.Subsets[0].Addresses) == 2
	})

	t.Run("BrowserGetsTheWakingPageThenTheApp", testBrowser)
	t.Run("APICallWaitsForTheAppWithItsBody", testAPICall)
	t.Run("BurstSharesOneWakeUp", testBurst)
	t.Run("WebSocketWaitsForTheApp", testWebSocket)
	t.Run("H2CReachesTheAppAsH2C", testH2C)
	t.Run("InspectorRecordsRequestsOfEveryReplica", testInspector)
	t.Run("ConditionsSendRequestsToAnotherBackend", testConditions)
	t.Run("ProtectionRequiresSignInOrToken", testProtection)
	t.Run("LongStreamKeepsTheAppAwake", testLongStream)
	t.Run("NewLeaderPutsTheIdleAppToSleep", testFailover)
	t.Run("UninstallRestoresEverything", testUninstall)
}

func testBrowser(t *testing.T) {
	sleepApp(t)
	resp, body := get(t, "/", "text/html")
	if resp.StatusCode != 200 || !strings.Contains(body, "__smart_proxy/status") {
		t.Fatalf("asleep: got %d %q", resp.StatusCode, truncate(body))
	}
	eventually(t, 2*time.Minute, "status never ready", func() bool {
		_, body := get(t, "/__smart_proxy/status?path=/", "application/json")
		return strings.Contains(body, `"ready"`)
	})
	if resp, body := get(t, "/", "text/html"); resp.StatusCode != 200 || !strings.Contains(body, "echoapp") {
		t.Fatalf("awake: got %d %q", resp.StatusCode, truncate(body))
	}
}

func testAPICall(t *testing.T) {
	sleepApp(t)
	payload := make([]byte, 20<<20)
	rand.Read(payload)
	sum := sha256.Sum256(payload)
	req, _ := http.NewRequest("POST", proxyURL+"/echo", bytes.NewReader(payload))
	req.Host = appHost
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Method string `json:"method"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != 200 || got.Bytes != int64(len(payload)) || got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("got %d %+v", resp.StatusCode, got)
	}
	t.Logf("20 MiB POST to a sleeping app answered by the app after %s", time.Since(start).Round(100*time.Millisecond))
}

func testBurst(t *testing.T) {
	sleepApp(t)
	since := metav1.NewTime(time.Now().Add(-time.Second))
	const n = 200
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", proxyURL+"/echo", nil)
			req.Host = appHost
			req.Header.Set("Accept", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != 200 {
				failed.Add(1)
			}
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if failed.Load() > 0 {
		t.Fatalf("%d of %d requests failed", failed.Load(), n)
	}
	if wakes := countLogs(t, since, "Waking up e2e/echo"); wakes != 1 {
		t.Fatalf("%d wake-ups logged for %d concurrent requests, want 1", wakes, n)
	}
}

func testWebSocket(t *testing.T) {
	sleepApp(t)
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Minute))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", appHost)
	r := bufio.NewReader(conn)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, " 101 ") {
		t.Fatalf("handshake answered %q", status)
	}
	for line, _ := r.ReadString('\n'); strings.TrimSpace(line) != ""; line, _ = r.ReadString('\n') {
	}
	io.WriteString(conn, "ping\n")
	if echo, _ := r.ReadString('\n'); echo != "ping\n" {
		t.Fatalf("echo %q", echo)
	}
}

func testH2C(t *testing.T) {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}}
	req, _ := http.NewRequest("POST", directURL+"/echo", nil)
	req.Host = appHost
	req.Header.Set("Content-Type", "application/grpc") // gRPC is what goes to applications as h2c
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Proto string `json:"proto"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Proto != "HTTP/2.0" {
		t.Fatalf("the app saw %q", got.Proto)
	}
}

// A stream open for longer than the idle timeout, with no other traffic, keeps the app awake.
func testLongStream(t *testing.T) {
	get(t, "/echo", "application/json") // Awake
	seconds := int(2*idleLimit/time.Second) + 30
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/sse?seconds=%d", proxyURL, seconds), nil)
	req.Host = appHost
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	stop := make(chan struct{})
	slept := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Second):
				if replicas(t) == 0 {
					slept <- struct{}{}
					return
				}
			}
		}
	}()
	events := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			events++
		}
	}
	close(stop)
	select {
	case <-slept:
		t.Fatal("the app was put to sleep during the stream")
	default:
	}
	if events < seconds-2 {
		t.Fatalf("stream ended after %d of %d events", events, seconds)
	}
}

// The leader goes away: another replica takes over and still puts idle apps to sleep.
func testFailover(t *testing.T) {
	lease, err := kube.CoordinationV1().Leases(proxyNS).Get(context.TODO(), release+"-smart-proxy-leader", metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil {
		t.Fatalf("no leader: %v", err)
	}
	old := *lease.Spec.HolderIdentity
	if err := kube.CoreV1().Pods(proxyNS).Delete(context.TODO(), old, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Minute, "no new leader", func() bool {
		lease, err := kube.CoordinationV1().Leases(proxyNS).Get(context.TODO(), release+"-smart-proxy-leader", metav1.GetOptions{})
		return err == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" && *lease.Spec.HolderIdentity != old
	})
	eventually(t, idleLimit+2*time.Minute, "the idle app was not put to sleep after the failover", func() bool { return replicas(t) == 0 })
}

func testUninstall(t *testing.T) {
	out, err := exec.Command("helm", "uninstall", release, "-n", proxyNS, "--wait", "--timeout", "3m").CombinedOutput()
	if err != nil {
		t.Fatalf("helm uninstall: %v\n%s", err, out)
	}
	ing, err := kube.NetworkingV1().Ingresses(appNS).Get(context.TODO(), "echo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if backend.Name != "echo" || backend.Port.Number != 80 || ing.Annotations["smart-proxy/patched"] != "" {
		t.Fatalf("Ingress not restored: %+v %v", backend, ing.Annotations)
	}
	if r := replicas(t); r != 1 {
		t.Fatalf("app left with %d replicas", r)
	}
	if _, err := kube.CoreV1().Services(appNS).Get(context.TODO(), service, metav1.GetOptions{}); err == nil {
		t.Fatal("stand-in Service left behind")
	}
	eventually(t, 2*time.Minute, "the app is not reachable through its Ingress after uninstall", func() bool {
		resp, body := get(t, "/", "text/html")
		return resp.StatusCode == 200 && strings.Contains(body, "echoapp")
	})
}

func testInspector(t *testing.T) {
	adminJSON(t, "POST", "/api/routes/inspect?id="+url.QueryEscape(routeID)+"&minutes=5", nil, nil)
	defer adminJSON(t, "POST", "/api/routes/inspect?id="+url.QueryEscape(routeID)+"&minutes=0", nil, nil)
	time.Sleep(3 * time.Second) // Every replica sees the route change

	marker := fmt.Sprint(time.Now().UnixNano())
	for i := 0; i < 30; i++ {
		req, _ := http.NewRequest("GET", proxyURL+"/echo?q=1", nil)
		req.Host = appHost
		req.Header.Set("X-E2e-Marker", marker)
		req.Header.Set("Authorization", "Bearer must-not-be-shown")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	var got struct {
		Requests []struct {
			Replica string            `json:"replica"`
			Outcome string            `json:"outcome"`
			Headers map[string]string `json:"headers"`
			Query   []string          `json:"query"`
		} `json:"requests"`
		Replicas int `json:"replicas"`
	}
	adminJSON(t, "GET", "/api/routes/requests?id="+url.QueryEscape(routeID), nil, &got)
	seen, replicas := 0, map[string]bool{}
	for _, r := range got.Requests {
		if r.Headers["X-E2e-Marker"] != marker {
			continue
		}
		seen++
		replicas[r.Replica] = true
		if r.Headers["Authorization"] == "Bearer must-not-be-shown" || r.Outcome != "proxied" || len(r.Query) != 1 {
			t.Fatalf("recorded %+v", r)
		}
	}
	if seen != 30 || len(replicas) != 2 || got.Replicas != 2 {
		t.Fatalf("recorded %d of 30 requests from replicas %v (reported %d)", seen, replicas, got.Replicas)
	}
}

func testConditions(t *testing.T) {
	route := getRoute(t)
	route["backends"] = []map[string]any{
		{"service": "echo", "port": 80, "weight": 100, "workload": "echo", "managed": true},
		{"service": "echo-b", "port": 80, "weight": 0, "workload": "echo-b", "managed": false,
			"when": []map[string]any{{"field": "header", "name": "Origin", "op": "equals", "value": "https://idp.e2e.test"}}},
	}
	saveRoute(t, route)
	defer func() {
		route := getRoute(t)
		route["backends"] = []any{}
		saveRoute(t, route)
	}()
	time.Sleep(3 * time.Second)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	variant := func(method, path, origin string) string {
		req, _ := http.NewRequest(method, proxyURL+path, nil)
		req.Host = appHost
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var echo struct {
			Variant string `json:"variant"`
		}
		json.NewDecoder(resp.Body).Decode(&echo)
		return echo.Variant
	}
	// The jar keys cookies by URL host: requests go to localhost with Host echo.e2e.test.
	if v := variant("GET", "/echo", ""); v != "a" {
		t.Fatalf("ordinary request answered by %q", v)
	}
	if v := variant("POST", "/echo", "https://idp.e2e.test"); v != "b" {
		t.Fatalf("request from the identity provider answered by %q", v)
	}
	if v := variant("GET", "/echo", ""); v != "b" {
		t.Fatalf("follow-up answered by %q: the client wasn't kept on b", v)
	}
	variant("GET", "/__smart_proxy/use/default", "")
	if v := variant("GET", "/echo", ""); v != "a" {
		t.Fatalf("after use/default answered by %q", v)
	}
	variant("GET", "/__smart_proxy/use/echo-b", "")
	if v := variant("GET", "/echo", ""); v != "b" {
		t.Fatalf("after use/echo-b answered by %q", v)
	}
}

func testProtection(t *testing.T) {
	adminJSON(t, "POST", "/api/routes/protection/users", map[string]string{"id": routeID, "name": "team", "password": "e2e-password-1"}, nil)
	var created struct{ Token string }
	adminJSON(t, "POST", "/api/routes/protection/tokens", map[string]string{"id": routeID, "name": "ci"}, &created)
	route := getRoute(t)
	route["protection"] = map[string]any{"enabled": true, "open": []string{"/healthz"}}
	saveRoute(t, route)
	defer func() {
		route := getRoute(t)
		route["protection"] = nil
		saveRoute(t, route)
		adminJSON(t, "DELETE", "/api/routes/protection/users?id="+url.QueryEscape(routeID)+"&name=team", nil, nil)
		adminJSON(t, "DELETE", "/api/routes/protection/tokens?id="+url.QueryEscape(routeID)+"&name=ci", nil, nil)
	}()
	time.Sleep(3 * time.Second)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, header http.Header, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(method, proxyURL+path, strings.NewReader(body))
		req.Host = appHost
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var echo map[string]any
		json.NewDecoder(resp.Body).Decode(&echo)
		return resp, echo
	}

	if resp, _ := call("GET", "/echo", http.Header{"Accept": {"application/json"}}, ""); resp.StatusCode != 401 {
		t.Fatalf("without credentials: %d", resp.StatusCode)
	}
	if resp, _ := call("GET", "/healthz", nil, ""); resp.StatusCode != 200 {
		t.Fatalf("open path: %d", resp.StatusCode)
	}
	resp, echo := call("GET", "/echo", http.Header{"Authorization": {"Bearer " + created.Token}}, "")
	if resp.StatusCode != 200 || echo["user"] != "token:ci" || echo["authorization"] != "" {
		t.Fatalf("with the token: %d %v", resp.StatusCode, echo)
	}

	resp, _ = call("GET", "/orders", http.Header{"Accept": {"text/html"}}, "")
	if resp.StatusCode != 302 || !strings.Contains(resp.Header.Get("Location"), "/__smart_proxy/login") {
		t.Fatalf("browser without login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = call("POST", "/__smart_proxy/login", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		url.Values{"password": {"e2e-password-1"}, "next": {"/echo"}}.Encode())
	if resp.StatusCode != 303 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	// The jar keeps the login cookie for localhost; send it explicitly to the app host.
	var session string
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "sp_auth_") {
			session = c.Name + "=" + c.Value
		}
	}
	resp, echo = call("GET", "/echo", http.Header{"Cookie": {session + "; app=1"}}, "")
	if resp.StatusCode != 200 || echo["user"] != "team" || strings.Contains(fmt.Sprint(echo["cookies"]), "sp_auth_") {
		t.Fatalf("signed in: %d %v", resp.StatusCode, echo)
	}
}

// Helpers

func adminJSON(t *testing.T, method, path string, body, out any) {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, adminURL+path, bytes.NewReader(data))
	req.SetBasicAuth("admin", "e2e-only")
	req.Header.Set("Origin", adminURL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
}

func getRoute(t *testing.T) map[string]any {
	t.Helper()
	var routes []map[string]any
	adminJSON(t, "GET", "/api/routes", nil, &routes)
	for _, r := range routes {
		if r["id"] == routeID {
			return r
		}
	}
	t.Fatalf("route %s not found", routeID)
	return nil
}

func saveRoute(t *testing.T, route map[string]any) {
	t.Helper()
	adminJSON(t, "POST", "/api/routes", route, nil)
}

func admin(method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, adminURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("admin", "e2e-only")
	req.Header.Set("Origin", adminURL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

func setIdleTimeout(t *testing.T, d time.Duration) {
	req, _ := http.NewRequest("GET", adminURL+"/api/routes", nil)
	req.SetBasicAuth("admin", "e2e-only")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var routes []map[string]any
	json.NewDecoder(resp.Body).Decode(&routes)
	resp.Body.Close()
	for _, r := range routes {
		if r["id"] != routeID {
			continue
		}
		config := map[string]any{}
		for _, k := range []string{"id", "host", "path", "target_service", "target_port", "namespace", "deployment", "dependencies", "always_on", "start_in_order"} {
			config[k] = r[k]
		}
		config["idle_timeout"] = d.Nanoseconds()
		data, _ := json.Marshal(config)
		if resp, err := admin("POST", "/api/routes", data); err != nil || resp.StatusCode >= 300 {
			t.Fatalf("saving the route: %v %v", resp, err)
		}
		return
	}
	t.Fatalf("route %s not found", routeID)
}

func get(t *testing.T, path, accept string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", proxyURL+path, nil)
	req.Host = appHost
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func sleepApp(t *testing.T) {
	t.Helper()
	if resp, err := admin("POST", "/api/k8s/stop-deployment?namespace=e2e&deployment=echo", nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("stopping the app: %v %v", resp, err)
	}
	eventually(t, time.Minute, "the app's pods didn't stop", func() bool {
		pods, err := kube.CoreV1().Pods(appNS).List(context.TODO(), metav1.ListOptions{LabelSelector: "app=echo"})
		return err == nil && len(pods.Items) == 0
	})
	time.Sleep(4 * time.Second) // Let the wake-up memo of the replicas expire
}

func replicas(t *testing.T) int32 {
	d, err := kube.AppsV1().Deployments(appNS).Get(context.TODO(), "echo", metav1.GetOptions{})
	if err != nil {
		t.Error(err)
		return -1
	}
	return *d.Spec.Replicas
}

// countLogs counts the log lines of Smart Proxy's pods containing text since a time.
func countLogs(t *testing.T, since metav1.Time, text string) int {
	pods, err := kube.CoreV1().Pods(proxyNS).List(context.TODO(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=smart-proxy"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		data, err := kube.CoreV1().Pods(proxyNS).GetLogs(p.Name, &corev1.PodLogOptions{SinceTime: &since}).DoRaw(context.TODO())
		if err != nil {
			t.Fatal(err)
		}
		count += strings.Count(string(data), text)
	}
	return count
}

func eventually(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(time.Second)
	}
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
