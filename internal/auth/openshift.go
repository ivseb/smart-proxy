package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const openshiftStateCookie = "smart_proxy_openshift"

// openshiftClient signs users in with their OpenShift account, without an oauth-proxy sidecar:
// Smart Proxy's ServiceAccount is the OAuth client (its token is the client secret, its
// oauth-redirectreference annotation allows the dashboard's address), the cluster's OAuth server
// authenticates, and SelfSubjectAccessReviews made with the user's token (scope
// user:check-access) decide who gets in. Smart Proxy needs no cluster-wide permission.
type openshiftClient struct {
	cfg    Config
	signer signer
	http   *http.Client

	// The OAuth endpoints are discovered lazily, so Smart Proxy starts while they're unreachable.
	mu        sync.Mutex
	endpoints *oauth2.Endpoint
}

// openshiftState travels in a short-lived signed cookie between login and callback.
type openshiftState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Redirect string `json:"r"`
	Expires  int64  `json:"exp"`
}

// forbiddenError is a user who signed in but fails an access review.
type forbiddenError struct {
	User   string
	Review AccessReview
}

func (e *forbiddenError) Error() string {
	return fmt.Sprintf("%s can't %s in namespace %s", e.User, e.Review.describe(), e.Review.Namespace)
}

// describe renders a review's permission: "update services", "get deployments.apps/web".
func (r AccessReview) describe() string {
	resource := r.Resource
	if r.Subresource != "" {
		resource += "/" + r.Subresource
	}
	if r.Group != "" {
		resource += "." + r.Group
	}
	if r.Name != "" {
		resource += " " + r.Name
	}
	return r.Verb + " " + resource
}

func newOpenShiftClient(cfg Config, s signer) (*openshiftClient, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	// The ServiceAccount's CAs: the API server's and, on OpenShift, the ingress' (the OAuth server's
	// Route), plus the service CA. Then any CA configured.
	dir := filepath.Dir(cfg.OpenShiftTokenFile)
	for _, f := range []string{filepath.Join(dir, "ca.crt"), filepath.Join(dir, "service-ca.crt"), cfg.OpenShiftCAFile} {
		if f == "" {
			continue
		}
		pem, err := os.ReadFile(f)
		if err != nil {
			if f == cfg.OpenShiftCAFile {
				return nil, fmt.Errorf("reading AUTH_OPENSHIFT_CA_FILE: %w", err)
			}
			continue
		}
		pool.AppendCertsFromPEM(pem)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &openshiftClient{cfg: cfg, signer: s, http: &http.Client{Transport: transport, Timeout: 15 * time.Second}}, nil
}

// namespace is Smart Proxy's namespace, the default of the access reviews.
func (c *openshiftClient) namespace() string {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(c.cfg.OpenShiftTokenFile), "namespace"))
	return strings.TrimSpace(string(b))
}

// secret is the ServiceAccount's token, read each time: it rotates.
func (c *openshiftClient) secret() (string, error) {
	b, err := os.ReadFile(c.cfg.OpenShiftTokenFile)
	if err != nil {
		return "", fmt.Errorf("reading the ServiceAccount token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// clientID is the OAuth client: system:serviceaccount:<namespace>:<name>.
func (c *openshiftClient) clientID(token string) (string, error) {
	sa := c.cfg.OpenShiftServiceAccount
	if strings.HasPrefix(sa, "system:serviceaccount:") {
		return sa, nil
	}
	if sa != "" {
		return "system:serviceaccount:" + c.namespace() + ":" + sa, nil
	}
	// Smart Proxy's own ServiceAccount: the subject of its token.
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Sub string `json:"sub"`
			}
			if json.Unmarshal(payload, &claims) == nil && strings.HasPrefix(claims.Sub, "system:serviceaccount:") {
				return claims.Sub, nil
			}
		}
	}
	return "", errors.New("can't tell Smart Proxy's ServiceAccount: set AUTH_OPENSHIFT_SERVICE_ACCOUNT")
}

func (c *openshiftClient) discover(ctx context.Context) (oauth2.Endpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoints != nil {
		return *c.endpoints, nil
	}
	e := oauth2.Endpoint{AuthStyle: oauth2.AuthStyleInParams}
	if base := c.cfg.OpenShiftOAuthURL; base != "" {
		e.AuthURL, e.TokenURL = base+"/oauth/authorize", base+"/oauth/token"
	} else {
		var meta struct {
			Authorization string `json:"authorization_endpoint"`
			Token         string `json:"token_endpoint"`
		}
		if err := c.call(ctx, http.MethodGet, "/.well-known/oauth-authorization-server", "", nil, &meta); err != nil {
			return e, fmt.Errorf("discovering the OpenShift OAuth server: %w", err)
		}
		if meta.Authorization == "" || meta.Token == "" {
			return e, errors.New("the API server doesn't describe an OAuth server (not OpenShift? set AUTH_OPENSHIFT_BASE_URL)")
		}
		e.AuthURL, e.TokenURL = meta.Authorization, meta.Token
	}
	c.endpoints = &e
	return e, nil
}

// call makes a request to the API server, with a bearer token when given, and decodes the answer.
func (c *openshiftClient) call(ctx context.Context, method, path, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.OpenShiftAPIURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

func (c *openshiftClient) oauth2Config(e oauth2.Endpoint, redirect string) (*oauth2.Config, error) {
	secret, err := c.secret()
	if err != nil {
		return nil, err
	}
	id, err := c.clientID(secret)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{ClientID: id, ClientSecret: secret, RedirectURL: redirect, Endpoint: e, Scopes: []string{"user:info", "user:check-access"}}, nil
}

// redirectURL is where the OAuth server sends the user back: configured, or the address the
// dashboard was reached at (it must be one the ServiceAccount's annotation allows).
func (c *openshiftClient) redirectURL(r *http.Request) string {
	if c.cfg.OpenShiftRedirectURL != "" {
		return c.cfg.OpenShiftRedirectURL
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/auth/callback"
}

func (c *openshiftClient) startLogin(w http.ResponseWriter, r *http.Request) {
	e, err := c.discover(r.Context())
	if err != nil {
		logOpenShift("%v", err)
		http.Error(w, "OpenShift sign-in unavailable", http.StatusBadGateway)
		return
	}
	st := openshiftState{
		State:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Redirect: c.redirectURL(r),
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}
	conf, err := c.oauth2Config(e, st.Redirect)
	if err != nil {
		logOpenShift("%v", err)
		http.Error(w, "OpenShift sign-in unavailable", http.StatusInternalServerError)
		return
	}
	value, err := c.signer.sign(st)
	if err != nil {
		http.Error(w, "Failed to start login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     openshiftStateCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode, // Must survive the top-level redirect back from the OAuth server
	})
	http.Redirect(w, r, conf.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

// finishLogin validates the callback and returns the user, if every access review passes (a
// *forbiddenError otherwise). The user's token is only used here: it isn't kept.
func (c *openshiftClient) finishLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	cookie, err := r.Cookie(openshiftStateCookie)
	if err != nil {
		return "", errors.New("missing login state (cookie expired or blocked)")
	}
	clearCookie(w, r, openshiftStateCookie)

	var st openshiftState
	if err := c.signer.verify(cookie.Value, &st); err != nil || time.Now().Unix() > st.Expires {
		return "", errors.New("invalid or expired login state")
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return "", fmt.Errorf("OAuth server returned %s: %s", e, r.URL.Query().Get("error_description"))
	}
	if !secureEqual(r.URL.Query().Get("state"), st.State) {
		return "", errors.New("state mismatch")
	}

	e, err := c.discover(r.Context())
	if err != nil {
		return "", err
	}
	conf, err := c.oauth2Config(e, st.Redirect)
	if err != nil {
		return "", err
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, c.http)
	token, err := conf.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return "", fmt.Errorf("exchanging code: %w", err)
	}

	var user struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := c.call(r.Context(), http.MethodGet, "/apis/user.openshift.io/v1/users/~", token.AccessToken, nil, &user); err != nil {
		return "", fmt.Errorf("reading the user: %w", err)
	}
	name := user.Metadata.Name
	if name == "" {
		return "", errors.New("the OAuth server returned no user name")
	}

	for _, review := range c.cfg.OpenShiftAccessReviews {
		if review.Namespace == "" {
			review.Namespace = c.namespace()
		}
		allowed, err := c.review(r.Context(), token.AccessToken, review)
		if err != nil {
			return "", fmt.Errorf("checking %s's access: %w", name, err)
		}
		if !allowed {
			return "", &forbiddenError{User: name, Review: review}
		}
	}
	return name, nil
}

// review asks the API server, as the user, whether the user may do what a review says.
func (c *openshiftClient) review(ctx context.Context, token string, r AccessReview) (bool, error) {
	body := map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec": map[string]any{
			"resourceAttributes": map[string]string{
				"namespace": r.Namespace, "verb": r.Verb, "group": r.Group,
				"resource": r.Resource, "subresource": r.Subresource, "name": r.Name,
			},
		},
	}
	var out struct {
		Status struct {
			Allowed bool `json:"allowed"`
		} `json:"status"`
	}
	err := c.call(ctx, http.MethodPost, "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", token, body, &out)
	return out.Status.Allowed, err
}
