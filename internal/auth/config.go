package auth

import (
	"fmt"
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
	// (OpenShift oauth-proxy, oauth2-proxy, Authelia...).
	ModeHeader Mode = "header"
)

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
}

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
	default:
		return fmt.Errorf("unknown AUTH_MODE %q (want none, basic, token, oidc or header)", c.Mode)
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
