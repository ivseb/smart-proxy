package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcStateCookie = "smart_proxy_oidc"

// oidcClient runs the OpenID Connect authorization code flow (with PKCE).
type oidcClient struct {
	cfg    Config
	signer signer

	// The provider is discovered lazily, so Smart Proxy still starts while the IdP is unreachable.
	mu       sync.Mutex
	provider *oidc.Provider
}

// oidcState travels in a short-lived signed cookie between login and callback.
type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"exp"`
}

func newOIDCClient(cfg Config, s signer) *oidcClient {
	return &oidcClient{cfg: cfg, signer: s}
}

func (c *oidcClient) discover(ctx context.Context) (*oidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		p, err := oidc.NewProvider(ctx, c.cfg.OIDCIssuerURL)
		if err != nil {
			return nil, fmt.Errorf("discovering OIDC issuer %s: %w", c.cfg.OIDCIssuerURL, err)
		}
		c.provider = p
	}
	return c.provider, nil
}

func (c *oidcClient) oauth2Config(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.cfg.OIDCClientID,
		ClientSecret: c.cfg.OIDCClientSecret,
		RedirectURL:  c.cfg.OIDCRedirectURL,
		Endpoint:     p.Endpoint(),
		Scopes:       c.cfg.OIDCScopes,
	}
}

func (c *oidcClient) startLogin(w http.ResponseWriter, r *http.Request) {
	p, err := c.discover(r.Context())
	if err != nil {
		http.Error(w, "Identity provider unavailable", http.StatusBadGateway)
		return
	}

	st := oidcState{
		State:    randomString(),
		Nonce:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}
	value, err := c.signer.sign(st)
	if err != nil {
		http.Error(w, "Failed to start login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode, // Must survive the top-level redirect back from the IdP
	})

	authURL := c.oauth2Config(p).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// finishLogin validates the callback and returns the username of an allowed user.
func (c *oidcClient) finishLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		return "", errors.New("missing login state (cookie expired or blocked)")
	}
	clearCookie(w, r, oidcStateCookie)

	var st oidcState
	if err := c.signer.verify(cookie.Value, &st); err != nil || time.Now().Unix() > st.Expires {
		return "", errors.New("invalid or expired login state")
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return "", fmt.Errorf("provider returned %s: %s", e, r.URL.Query().Get("error_description"))
	}
	if !secureEqual(r.URL.Query().Get("state"), st.State) {
		return "", errors.New("state mismatch")
	}

	p, err := c.discover(r.Context())
	if err != nil {
		return "", err
	}
	token, err := c.oauth2Config(p).Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return "", fmt.Errorf("exchanging code: %w", err)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return "", errors.New("no id_token in token response")
	}
	idToken, err := p.Verifier(&oidc.Config{ClientID: c.cfg.OIDCClientID}).Verify(r.Context(), rawID)
	if err != nil {
		return "", fmt.Errorf("verifying id_token: %w", err)
	}
	if !secureEqual(idToken.Nonce, st.Nonce) {
		return "", errors.New("nonce mismatch")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return "", err
	}
	return c.authorize(claims, idToken.Subject)
}

// authorize applies the allow-lists. A user passes if any configured rule matches.
func (c *oidcClient) authorize(claims map[string]any, subject string) (string, error) {
	email, _ := claims["email"].(string)
	user := firstString(claims, c.cfg.OIDCUsernameClaim, "email", "preferred_username")
	if user == "" {
		user = subject
	}

	if c.cfg.OIDCAllowAll {
		return user, nil
	}

	// Email-based rules only count for verified addresses (when the provider says so).
	if verified, ok := claims["email_verified"].(bool); email != "" && (!ok || verified) {
		for _, allowed := range c.cfg.OIDCAllowedEmails {
			if strings.EqualFold(email, allowed) {
				return user, nil
			}
		}
		if _, domain, found := strings.Cut(email, "@"); found {
			for _, allowed := range c.cfg.OIDCAllowedDomains {
				if strings.EqualFold(domain, strings.TrimPrefix(allowed, "@")) {
					return user, nil
				}
			}
		}
	}

	for _, group := range stringList(claims[c.cfg.OIDCGroupsClaim]) {
		for _, allowed := range c.cfg.OIDCAllowedGroups {
			if group == allowed {
				return user, nil
			}
		}
	}
	return "", fmt.Errorf("user %q is not in the allowed emails, domains or groups", user)
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// stringList accepts a claim encoded as a JSON array or as a single string.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
