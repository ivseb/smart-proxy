// Package auth protects the admin dashboard and API. The mode (none, basic, token, oidc or
// header) is chosen at install time; see Config.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"smart-proxy/internal/logger"
)

// Auth authenticates admin requests according to the configured Mode.
type Auth struct {
	cfg    Config
	signer signer
	oidc   *oidcClient
}

type userKey struct{}

// UserFromContext returns the authenticated user of an admin request, if any.
func UserFromContext(ctx context.Context) string {
	user, _ := ctx.Value(userKey{}).(string)
	return user
}

// New builds an Auth from a validated Config.
func New(cfg Config) (*Auth, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	key := []byte(cfg.SessionSecret)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if cfg.Mode == ModeToken || cfg.Mode == ModeOIDC {
			logger.Println("Auth: AUTH_SESSION_SECRET not set, using a random one (sessions end on restart)")
		}
	}

	a := &Auth{cfg: cfg, signer: signer{key: key}}
	switch cfg.Mode {
	case ModeNone:
		logger.Println("Auth: WARNING admin dashboard has NO authentication (AUTH_MODE=none). Do not expose it.")
	case ModeHeader:
		logger.Printf("Auth: trusting the %s header; make sure the admin port is only reachable through the auth proxy", cfg.HeaderUser)
	case ModeOIDC:
		a.oidc = newOIDCClient(cfg, a.signer)
	}
	logger.Printf("Auth: admin dashboard authentication mode is %q", cfg.Mode)
	return a, nil
}

// RegisterRoutes adds the /auth/ endpoints, which are reachable without authentication.
func (a *Auth) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/login", a.handleLogin)
	mux.HandleFunc("/auth/callback", a.handleCallback)
	mux.HandleFunc("/auth/logout", a.handleLogout)
	mux.HandleFunc("/auth/me", a.handleMe)
}

// Wrap protects every route of next except /auth/.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}

		user, ok := a.authenticate(r)
		if !ok {
			a.unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func (a *Auth) authenticate(r *http.Request) (string, bool) {
	switch a.cfg.Mode {
	case ModeNone:
		return "", true
	case ModeBasic:
		user, pass, ok := r.BasicAuth()
		if ok && secureEqual(user, a.cfg.BasicUsername) && secureEqual(pass, a.cfg.BasicPassword) {
			return user, true
		}
	case ModeToken:
		// Bearer tokens let scripts call the API; browsers use the session cookie set at login.
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if secureEqual(token, a.cfg.Token) {
				return "token", true
			}
			return "", false
		}
		return a.sessionUser(r)
	case ModeOIDC:
		return a.sessionUser(r)
	case ModeHeader:
		if user := r.Header.Get(a.cfg.HeaderUser); user != "" {
			return user, true
		}
	}
	return "", false
}

func (a *Auth) unauthorized(w http.ResponseWriter, r *http.Request) {
	api := strings.HasPrefix(r.URL.Path, "/api/")
	switch {
	case a.cfg.Mode == ModeBasic:
		// Lets the browser show its native login prompt, including for EventSource streams.
		w.Header().Set("WWW-Authenticate", `Basic realm="Smart Proxy", charset="UTF-8"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	case api:
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	case a.cfg.Mode == ModeHeader:
		http.Error(w, fmt.Sprintf("Unauthorized: missing %s header from the authentication proxy", a.cfg.HeaderUser), http.StatusUnauthorized)
	default:
		http.Redirect(w, r, "/auth/login", http.StatusFound)
	}
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch a.cfg.Mode {
	case ModeToken:
		a.handleTokenLogin(w, r)
	case ModeOIDC:
		a.oidc.startLogin(w, r)
	default:
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

func (a *Auth) handleTokenLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		renderPage(w, http.StatusOK, pageData{Form: true})
		return
	}
	if !secureEqual(r.PostFormValue("token"), a.cfg.Token) {
		logger.Printf("Auth: failed token login from %s", r.RemoteAddr)
		renderPage(w, http.StatusUnauthorized, pageData{Form: true, Error: "Invalid token"})
		return
	}
	if err := a.setSession(w, r, "token"); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) handleCallback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != ModeOIDC {
		http.NotFound(w, r)
		return
	}
	user, err := a.oidc.finishLogin(w, r)
	if err != nil {
		logger.Printf("Auth: OIDC login failed: %v", err)
		renderPage(w, http.StatusForbidden, pageData{Error: "Sign-in failed or not allowed.", Retry: true})
		return
	}
	if err := a.setSession(w, r, user); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}
	logger.Printf("Auth: %s signed in", user)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	switch a.cfg.Mode {
	case ModeToken, ModeOIDC:
		clearCookie(w, r, sessionCookie)
		renderPage(w, http.StatusOK, pageData{Message: "You have been signed out.", Retry: true})
	case ModeHeader:
		if a.cfg.HeaderLogoutURL != "" {
			http.Redirect(w, r, a.cfg.HeaderLogoutURL, http.StatusFound)
			return
		}
		fallthrough
	default:
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// handleMe tells the dashboard who is signed in and whether it can offer a logout link.
func (a *Auth) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticate(r)
	logoutURL := ""
	switch a.cfg.Mode {
	case ModeToken, ModeOIDC:
		logoutURL = "/auth/logout"
	case ModeHeader:
		logoutURL = a.cfg.HeaderLogoutURL
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"mode":          a.cfg.Mode,
		"authenticated": ok,
		"user":          user,
		"logout_url":    logoutURL,
	})
}

// sameOrigin rejects state-changing browser requests coming from another site (CSRF).
// Basic auth and cookies are sent automatically by browsers, so they need this check.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type pageData struct {
	Form    bool
	Error   string
	Message string
	Retry   bool
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Smart Proxy · Sign in</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0;font-family:system-ui,-apple-system,sans-serif}
.card{width:100%;max-width:360px;margin:16px;padding:32px;background:#1e293b;border:1px solid #334155;border-radius:16px}
h1{margin:0 0 20px;font-size:20px}
input{box-sizing:border-box;width:100%;padding:10px 12px;margin-bottom:12px;background:#0f172a;color:#e2e8f0;border:1px solid #475569;border-radius:8px;font-size:14px}
button,a.btn{display:block;box-sizing:border-box;width:100%;padding:10px;background:#3b82f6;color:#fff;border:0;border-radius:8px;font-size:14px;font-weight:600;text-align:center;text-decoration:none;cursor:pointer}
.err{color:#fca5a5;margin-bottom:12px;font-size:14px}.msg{margin-bottom:16px;font-size:14px}
</style></head><body><div class="card"><h1>⚡ Smart Proxy</h1>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
{{if .Message}}<div class="msg">{{.Message}}</div>{{end}}
{{if .Form}}<form method="post" action="/auth/login">
<input type="password" name="token" placeholder="Access token" autocomplete="current-password" autofocus required>
<button type="submit">Sign in</button></form>{{end}}
{{if .Retry}}<a class="btn" href="/auth/login">Sign in</a>{{end}}
</div></body></html>`))

func renderPage(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	pageTmpl.Execute(w, data)
}
