package auth

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// Mode selects how the admin dashboard authenticates users.
type Mode string

const (
	// ModeNone disables authentication. Only safe when the admin port is not reachable.
	ModeNone Mode = "none"
	// ModeBasic uses HTTP Basic auth with a single username/password.
	ModeBasic Mode = "basic"
	// ModeToken uses a shared token, entered on a login page or sent as a Bearer header.
	ModeToken Mode = "token"
	// ModeOIDC delegates login to an OpenID Connect provider (Keycloak, Entra ID, Google, Okta, Dex...).
	ModeOIDC Mode = "oidc"
	// ModeHeader trusts a user header set by an authenticating reverse proxy
	// (oauth2-proxy, Authelia...).
	ModeHeader Mode = "header"
	// ModeOpenShift signs users in with their OpenShift account (the cluster's OAuth server, Smart
	// Proxy's ServiceAccount as the OAuth client) and lets in those passing access reviews.
	ModeOpenShift Mode = "openshift"
)

// AccessReview is a permission a user must have to get in (OpenShift mode), checked with a
// SelfSubjectAccessReview made with the user's own token. Its JSON is oauth-proxy's
// --openshift-sar format, so existing settings keep working.
type AccessReview struct {
	Namespace   string `json:"namespace,omitempty"` // Empty: Smart Proxy's namespace
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"` // API group of the resource ("" for core)
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Name        string `json:"name,omitempty"`
}

func (r *AccessReview) UnmarshalJSON(data []byte) error {
	var v struct {
		Namespace        string `json:"namespace"`
		Verb             string `json:"verb"`
		Group            string `json:"group"`
		ResourceAPIGroup string `json:"resourceAPIGroup"` // oauth-proxy's spelling
		Resource         string `json:"resource"`
		Subresource      string `json:"subresource"`
		Name             string `json:"name"`
		ResourceName     string `json:"resourceName"` // oauth-proxy's spelling
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*r = AccessReview{Namespace: v.Namespace, Verb: v.Verb, Group: v.Group, Resource: v.Resource, Subresource: v.Subresource, Name: v.Name}
	if r.Group == "" {
		r.Group = v.ResourceAPIGroup
	}
	if r.Name == "" {
		r.Name = v.ResourceName
	}
	return nil
}

// parseAccessReviews reads one review (a JSON object) or several (an array, all required).
func parseAccessReviews(s string) ([]AccessReview, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var list []AccessReview
		err := json.Unmarshal([]byte(s), &list)
		return list, err
	}
	var one AccessReview
	if err := json.Unmarshal([]byte(s), &one); err != nil {
		return nil, err
	}
	return []AccessReview{one}, nil
}

// Config holds the authentication settings, usually read from AUTH_* environment variables.
type Config struct {
	Mode Mode

	BasicUsername string
	BasicPassword string

	Token string

	// SessionSecret signs session cookies (token and oidc modes). Random when empty,
	// which logs everyone out on restart.
	SessionSecret string
	SessionTTL    time.Duration

	OIDCIssuerURL     string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	OIDCScopes        []string
	OIDCUsernameClaim string
	OIDCGroupsClaim   string
	OIDCAllowedEmails []string
	// OIDCAllowedDomains matches the domain part of the user's email.
	OIDCAllowedDomains []string
	OIDCAllowedGroups  []string
	// OIDCAllowAll lets in every user the provider authenticates. Required when no
	// allow-list is set, so that e.g. a Google issuer doesn't admit any Google account by accident.
	OIDCAllowAll bool

	HeaderUser      string
	HeaderLogoutURL string

	// OpenShift mode: the access reviews a user must pass (all of them).
	OpenShiftAccessReviews []AccessReview
	// The OAuth client: "system:serviceaccount:<namespace>:<name>", or just the ServiceAccount's
	// name. Empty: Smart Proxy's own ServiceAccount, read from its token.
	OpenShiftServiceAccount string
	// The OAuth server (https://oauth-openshift.apps.<cluster>), when it can't be discovered from
	// the API server.
	OpenShiftOAuthURL string
	// The API server; in a pod, the cluster's (KUBERNETES_SERVICE_HOST).
	OpenShiftAPIURL string
	// Where the OAuth server sends users back (https://<dashboard>/auth/callback). Empty: the
	// address the dashboard was reached at.
	OpenShiftRedirectURL string
	// More CA certificates to trust (PEM), besides the system's and the ServiceAccount's.
	OpenShiftCAFile string
	// The ServiceAccount's token (the OAuth client's secret), read at each sign-in: it rotates.
	OpenShiftTokenFile string
}

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// LoadConfig reads the configuration through getenv (typically os.Getenv) and validates it.
func LoadConfig(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}

	cfg := Config{
		Mode:               Mode(strings.ToLower(get("AUTH_MODE", string(ModeNone)))),
		BasicUsername:      get("AUTH_BASIC_USERNAME", "admin"),
		BasicPassword:      getenv("AUTH_BASIC_PASSWORD"),
		Token:              getenv("AUTH_TOKEN"),
		SessionSecret:      getenv("AUTH_SESSION_SECRET"),
		OIDCIssuerURL:      get("AUTH_OIDC_ISSUER_URL", ""),
		OIDCClientID:       get("AUTH_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:   getenv("AUTH_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:    get("AUTH_OIDC_REDIRECT_URL", ""),
		OIDCScopes:         splitList(get("AUTH_OIDC_SCOPES", "openid,email,profile")),
		OIDCUsernameClaim:  get("AUTH_OIDC_USERNAME_CLAIM", "email"),
		OIDCGroupsClaim:    get("AUTH_OIDC_GROUPS_CLAIM", "groups"),
		OIDCAllowedEmails:  splitList(getenv("AUTH_OIDC_ALLOWED_EMAILS")),
		OIDCAllowedDomains: splitList(getenv("AUTH_OIDC_ALLOWED_DOMAINS")),
		OIDCAllowedGroups:  splitList(getenv("AUTH_OIDC_ALLOWED_GROUPS")),
		OIDCAllowAll:       strings.EqualFold(get("AUTH_OIDC_ALLOW_ALL", "false"), "true"),
		HeaderUser:         get("AUTH_HEADER_USER", "X-Forwarded-User"),
		HeaderLogoutURL:    get("AUTH_HEADER_LOGOUT_URL", ""),
	}

	cfg.OpenShiftServiceAccount = get("AUTH_OPENSHIFT_SERVICE_ACCOUNT", "")
	cfg.OpenShiftOAuthURL = strings.TrimRight(get("AUTH_OPENSHIFT_BASE_URL", ""), "/")
	cfg.OpenShiftAPIURL = strings.TrimRight(get("AUTH_OPENSHIFT_API_URL", ""), "/")
	if cfg.OpenShiftAPIURL == "" && getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg.OpenShiftAPIURL = "https://" + net.JoinHostPort(getenv("KUBERNETES_SERVICE_HOST"), get("KUBERNETES_SERVICE_PORT", "443"))
	}
	cfg.OpenShiftRedirectURL = get("AUTH_OPENSHIFT_REDIRECT_URL", "")
	cfg.OpenShiftCAFile = get("AUTH_OPENSHIFT_CA_FILE", "")
	cfg.OpenShiftTokenFile = get("AUTH_OPENSHIFT_TOKEN_FILE", serviceAccountDir+"/token")
	if cfg.Mode == ModeOpenShift {
		// AUTH_OPENSHIFT_SAR (JSON, oauth-proxy's format), or one review from the separate variables.
		if sar := getenv("AUTH_OPENSHIFT_SAR"); strings.TrimSpace(sar) != "" {
			reviews, err := parseAccessReviews(sar)
			if err != nil {
				return cfg, fmt.Errorf("invalid AUTH_OPENSHIFT_SAR: %v", err)
			}
			cfg.OpenShiftAccessReviews = reviews
		} else {
			cfg.OpenShiftAccessReviews = []AccessReview{{
				Namespace: get("AUTH_OPENSHIFT_SAR_NAMESPACE", ""),
				Verb:      get("AUTH_OPENSHIFT_SAR_VERB", "update"),
				Resource:  get("AUTH_OPENSHIFT_SAR_RESOURCE", "services"),
			}}
		}
	}

	ttl, err := time.ParseDuration(get("AUTH_SESSION_TTL", "12h"))
	if err != nil || ttl <= 0 {
		return cfg, fmt.Errorf("invalid AUTH_SESSION_TTL %q", getenv("AUTH_SESSION_TTL"))
	}
	cfg.SessionTTL = ttl

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	switch c.Mode {
	case ModeNone, ModeHeader:
		return nil
	case ModeBasic:
		if c.BasicPassword == "" {
			return fmt.Errorf("AUTH_MODE=basic requires AUTH_BASIC_PASSWORD")
		}
	case ModeToken:
		if c.Token == "" {
			return fmt.Errorf("AUTH_MODE=token requires AUTH_TOKEN")
		}
	case ModeOIDC:
		var missing []string
		for _, f := range []struct{ key, value string }{
			{"AUTH_OIDC_ISSUER_URL", c.OIDCIssuerURL},
			{"AUTH_OIDC_CLIENT_ID", c.OIDCClientID},
			{"AUTH_OIDC_CLIENT_SECRET", c.OIDCClientSecret},
			{"AUTH_OIDC_REDIRECT_URL", c.OIDCRedirectURL},
		} {
			if f.value == "" {
				missing = append(missing, f.key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("AUTH_MODE=oidc requires %s", strings.Join(missing, ", "))
		}
		if !c.OIDCAllowAll && len(c.OIDCAllowedEmails)+len(c.OIDCAllowedDomains)+len(c.OIDCAllowedGroups) == 0 {
			return fmt.Errorf("AUTH_MODE=oidc requires AUTH_OIDC_ALLOWED_EMAILS, AUTH_OIDC_ALLOWED_DOMAINS, AUTH_OIDC_ALLOWED_GROUPS or AUTH_OIDC_ALLOW_ALL=true")
		}
	case ModeOpenShift:
		if c.OpenShiftAPIURL == "" {
			return fmt.Errorf("AUTH_MODE=openshift requires running in the cluster or AUTH_OPENSHIFT_API_URL")
		}
		if len(c.OpenShiftAccessReviews) == 0 {
			return fmt.Errorf("AUTH_MODE=openshift requires at least one access review (AUTH_OPENSHIFT_SAR)")
		}
		for _, r := range c.OpenShiftAccessReviews {
			if r.Verb == "" || r.Resource == "" {
				return fmt.Errorf("every OpenShift access review needs a verb and a resource: %+v", r)
			}
		}
	default:
		return fmt.Errorf("unknown AUTH_MODE %q (want none, basic, token, oidc, openshift or header)", c.Mode)
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
