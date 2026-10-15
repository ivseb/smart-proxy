package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeOpenShift is the cluster's API server and OAuth server in one: discovery, token exchange,
// users/~ and SelfSubjectAccessReviews, for two users: alice (edit in team-a) and bob (view).
type fakeOpenShift struct {
	*httptest.Server
	secret string // The ServiceAccount token the OAuth client must present

	mu       sync.Mutex
	reviews  []map[string]string // resourceAttributes of the reviews asked
	verifier map[string]string   // code -> PKCE challenge
}

func newFakeOpenShift(t *testing.T) *fakeOpenShift {
	t.Helper()
	f := &fakeOpenShift{verifier: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 f.URL,
			"authorization_endpoint": f.URL + "/oauth/authorize",
			"token_endpoint":         f.URL + "/oauth/token",
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		code := r.PostForm.Get("code")
		f.mu.Lock()
		challenge := f.verifier[code]
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.PostForm.Get("client_id") != "system:serviceaccount:sp:smart-proxy" || r.PostForm.Get("client_secret") != f.secret ||
			r.PostForm.Get("grant_type") != "authorization_code" || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "sha256~" + code, "token_type": "Bearer", "expires_in": 86400})
	})
	user := func(r *http.Request) string {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer sha256~")
	}
	mux.HandleFunc("/apis/user.openshift.io/v1/users/~", func(w http.ResponseWriter, r *http.Request) {
		if u := user(r); u == "alice" || u == "bob" {
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": u}})
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", func(w http.ResponseWriter, r *http.Request) {
		var review struct {
			Kind string `json:"kind"`
			Spec struct {
				ResourceAttributes map[string]string `json:"resourceAttributes"`
			} `json:"spec"`
		}
		json.NewDecoder(r.Body).Decode(&review)
		attrs := review.Spec.ResourceAttributes
		f.mu.Lock()
		f.reviews = append(f.reviews, attrs)
		f.mu.Unlock()
		u := user(r)
		allowed := review.Kind == "SelfSubjectAccessReview" && attrs["namespace"] == "team-a" &&
			(attrs["verb"] == "get" && (u == "alice" || u == "bob") || attrs["verb"] == "update" && u == "alice")
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"allowed": allowed}})
	})
	f.Server = httptest.NewTLSServer(mux)
	t.Cleanup(f.Close)
	return f
}

// authorize plays the user signing in on the OAuth server: it returns the callback URL.
func (f *fakeOpenShift) authorize(t *testing.T, location, user string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, f.URL+"/oauth/authorize?") {
		t.Fatalf("not sent to the OAuth server: %s", location)
	}
	q := u.Query()
	if q.Get("client_id") != "system:serviceaccount:sp:smart-proxy" || q.Get("response_type") != "code" ||
		q.Get("scope") != "user:info user:check-access" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize request: %v", q)
	}
	f.mu.Lock()
	f.verifier[user] = q.Get("code_challenge")
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?" + url.Values{"code": {user}, "state": {q.Get("state")}}.Encode()
}

// openshiftConfig points a Config at the fake cluster, with a ServiceAccount directory (token,
// namespace) and the fake's CA.
func openshiftConfig(t *testing.T, f *fakeOpenShift, reviews ...AccessReview) Config {
	t.Helper()
	dir := t.TempDir()
	// A ServiceAccount token: a JWT whose subject is the ServiceAccount (the signature isn't checked).
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"system:serviceaccount:sp:smart-proxy"}`))
	f.secret = "eyJhbGciOiJSUzI1NiJ9." + claims + ".c2ln"
	os.WriteFile(filepath.Join(dir, "token"), []byte(f.secret+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "namespace"), []byte("team-a"), 0o600)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})
	os.WriteFile(filepath.Join(dir, "ca.crt"), ca, 0o600)
	if len(reviews) == 0 {
		reviews = []AccessReview{{Verb: "update", Resource: "services"}}
	}
	return Config{Mode: ModeOpenShift, OpenShiftAPIURL: f.URL, OpenShiftTokenFile: filepath.Join(dir, "token"), OpenShiftAccessReviews: reviews}
}

// openshiftLogin signs a user in through the dashboard and returns the callback's answer.
func openshiftLogin(t *testing.T, f *fakeOpenShift, srv *httptest.Server, user string) (*http.Response, string) {
	t.Helper()
	client := noRedirectClient()
	resp, _ := get(t, client, srv.URL+"/api/ping")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API without session: %d", resp.StatusCode)
	}
	resp, _ = get(t, client, srv.URL+"/")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/auth/login" {
		t.Fatalf("dashboard without session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = get(t, client, srv.URL+"/auth/login")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	state := resp.Cookies()
	callback := f.authorize(t, resp.Header.Get("Location"), user)
	if !strings.HasPrefix(callback, srv.URL+"/auth/callback?") {
		t.Fatalf("redirect_uri: %s", callback)
	}
	return get(t, client, callback, func(r *http.Request) {
		for _, c := range state {
			r.AddCookie(c)
		}
	})
}

func TestOpenShiftLoginLetsInUsersPassingTheAccessReview(t *testing.T) {
	f := newFakeOpenShift(t)
	srv := newTestServer(t, openshiftConfig(t, f))

	resp, _ := openshiftLogin(t, f, srv, "alice")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("alice's callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie")
	}
	_, body := get(t, noRedirectClient(), srv.URL+"/api/ping", func(r *http.Request) { r.AddCookie(session) })
	if body != "pong:alice" {
		t.Fatalf("with alice's session: %q", body)
	}
	// The review was the default one, in Smart Proxy's namespace, made as the user.
	f.mu.Lock()
	review := f.reviews[0]
	f.mu.Unlock()
	if review["namespace"] != "team-a" || review["verb"] != "update" || review["resource"] != "services" {
		t.Fatalf("review asked: %v", review)
	}
}

func TestOpenShiftLoginRefusesUsersFailingTheAccessReview(t *testing.T) {
	f := newFakeOpenShift(t)
	srv := newTestServer(t, openshiftConfig(t, f))

	resp, body := openshiftLogin(t, f, srv, "bob")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "Signed in as bob") || !strings.Contains(body, "update services in the namespace team-a") {
		t.Fatalf("bob's callback: %d %s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("bob got a session")
		}
	}
}

func TestOpenShiftAccessReviewsAllApply(t *testing.T) {
	f := newFakeOpenShift(t)
	// bob can get services, but not update them: both are required.
	reviews, err := parseAccessReviews(`[{"verb":"get","resource":"services"},{"namespace":"team-a","verb":"update","resource":"services"}]`)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, openshiftConfig(t, f, reviews...))
	if resp, _ := openshiftLogin(t, f, srv, "bob"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob: %d", resp.StatusCode)
	}
	if resp, _ := openshiftLogin(t, f, srv, "alice"); resp.StatusCode != http.StatusFound {
		t.Fatalf("alice: %d", resp.StatusCode)
	}
}

func TestOpenShiftCallbackRejectsForgedState(t *testing.T) {
	f := newFakeOpenShift(t)
	srv := newTestServer(t, openshiftConfig(t, f))
	client := noRedirectClient()
	resp, _ := get(t, client, srv.URL+"/auth/login")
	state := resp.Cookies()
	callback := f.authorize(t, resp.Header.Get("Location"), "alice")
	forged := strings.Replace(callback, "state=", "state=x", 1)
	resp, _ = get(t, client, forged, func(r *http.Request) {
		for _, c := range state {
			r.AddCookie(c)
		}
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged state: %d", resp.StatusCode)
	}
	// Without the state cookie (another browser) too.
	if resp, _ = get(t, client, callback); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no state cookie: %d", resp.StatusCode)
	}
}

func TestOpenShiftLogoutAndMe(t *testing.T) {
	f := newFakeOpenShift(t)
	srv := newTestServer(t, openshiftConfig(t, f))
	_, body := get(t, noRedirectClient(), srv.URL+"/auth/me")
	var me map[string]any
	json.Unmarshal([]byte(body), &me)
	if me["mode"] != "openshift" || me["logout_url"] != "/auth/logout" || me["authenticated"] != false {
		t.Fatalf("me: %v", me)
	}
	resp, _ := get(t, noRedirectClient(), srv.URL+"/auth/logout")
	cleared := false
	for _, c := range resp.Cookies() {
		cleared = cleared || c.Name == sessionCookie && c.MaxAge < 0
	}
	if resp.StatusCode != http.StatusOK || !cleared {
		t.Fatalf("logout: %d cleared=%v", resp.StatusCode, cleared)
	}
}

func TestOpenShiftConfig(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

	// In a pod: the API server from the service variables, the default review.
	cfg, err := LoadConfig(env(map[string]string{"AUTH_MODE": "openshift", "KUBERNETES_SERVICE_HOST": "10.0.0.1", "KUBERNETES_SERVICE_PORT": "443"}))
	if err != nil || cfg.OpenShiftAPIURL != "https://10.0.0.1:443" || len(cfg.OpenShiftAccessReviews) != 1 ||
		cfg.OpenShiftAccessReviews[0] != (AccessReview{Verb: "update", Resource: "services"}) {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	// oauth-proxy's --openshift-sar format.
	cfg, err = LoadConfig(env(map[string]string{"AUTH_MODE": "openshift", "AUTH_OPENSHIFT_API_URL": "https://api:6443",
		"AUTH_OPENSHIFT_SAR": `{"namespace":"tools","resource":"deployments","resourceAPIGroup":"apps","resourceName":"web","verb":"patch"}`}))
	if err != nil || cfg.OpenShiftAccessReviews[0] != (AccessReview{Namespace: "tools", Verb: "patch", Group: "apps", Resource: "deployments", Name: "web"}) {
		t.Fatalf("oauth-proxy format: %+v %v", cfg.OpenShiftAccessReviews, err)
	}
	// The separate variables.
	cfg, err = LoadConfig(env(map[string]string{"AUTH_MODE": "openshift", "AUTH_OPENSHIFT_API_URL": "https://api:6443",
		"AUTH_OPENSHIFT_SAR_NAMESPACE": "tools", "AUTH_OPENSHIFT_SAR_RESOURCE": "configmaps", "AUTH_OPENSHIFT_SAR_VERB": "get"}))
	if err != nil || cfg.OpenShiftAccessReviews[0] != (AccessReview{Namespace: "tools", Verb: "get", Resource: "configmaps"}) {
		t.Fatalf("separate variables: %+v %v", cfg.OpenShiftAccessReviews, err)
	}
	for name, vars := range map[string]map[string]string{
		"outside a cluster": {"AUTH_MODE": "openshift"},
		"invalid JSON":      {"AUTH_MODE": "openshift", "AUTH_OPENSHIFT_API_URL": "https://api", "AUTH_OPENSHIFT_SAR": "{"},
		"no verb":           {"AUTH_MODE": "openshift", "AUTH_OPENSHIFT_API_URL": "https://api", "AUTH_OPENSHIFT_SAR": `{"resource":"services"}`},
	} {
		if _, err := LoadConfig(env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOpenShiftClientID(t *testing.T) {
	c := &openshiftClient{cfg: Config{OpenShiftTokenFile: "/nonexistent/token"}}
	if _, err := c.clientID("not-a-jwt"); err == nil {
		t.Fatal("a token without a subject gave a client ID")
	}
	c.cfg.OpenShiftServiceAccount = "system:serviceaccount:sp:dashboard"
	if id, _ := c.clientID(""); id != "system:serviceaccount:sp:dashboard" {
		t.Fatalf("explicit: %s", id)
	}
}
