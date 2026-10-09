package guard

import (
	"html/template"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"smart-proxy/internal/store"
)

// LoginURL is where a browser without a login is sent, coming back to where it was going.
func LoginURL(route store.RouteConfig, r *http.Request) string {
	return basePath(route) + LoginPath + "?" + url.Values{"next": {r.URL.RequestURI()}}.Encode()
}

func basePath(route store.RouteConfig) string {
	return strings.TrimSuffix(route.Path, "/")
}

// cookiePath scopes the login cookie to the route ("/app" covers "/app" and "/app/...").
func cookiePath(route store.RouteConfig) string {
	if p := basePath(route); p != "" {
		return p
	}
	return "/"
}

// safeNext keeps a redirect after login on the same site: a path, without scheme or host, no
// control characters or backslashes (browsers drop or rewrite those into "//evil.com").
func safeNext(next string, route store.RouteConfig) string {
	fallback := basePath(route) + "/"
	if next == "" || strings.ContainsAny(next, "\\") || strings.IndexFunc(next, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fallback
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") ||
		strings.HasPrefix(next, "//") || strings.HasPrefix(path.Clean(u.Path), "//") {
		return fallback
	}
	return next
}

// ServeLogin shows the login page (GET) or checks a login (POST).
func (g *Guard) ServeLogin(w http.ResponseWriter, r *http.Request, route store.RouteConfig, client string) {
	creds := g.Vault.Credentials(route.ID)
	users := make([]string, 0, len(creds.Users))
	for name := range creds.Users {
		users = append(users, name)
	}
	sort.Strings(users)
	data := loginPage{
		Host:    strings.Split(r.Host, ":")[0],
		Action:  basePath(route) + LoginPath,
		Next:    safeNext(r.URL.Query().Get("next"), route),
		AskName: len(users) != 1, // A single user is a shared password: no name to type
	}
	if len(users) == 0 {
		data.Error = "Nobody can sign in yet: add a user to this route in Smart Proxy's dashboard."
		renderLogin(w, http.StatusForbidden, data)
		return
	}
	if r.Method != http.MethodPost {
		renderLogin(w, http.StatusOK, data)
		return
	}

	// Only this page posts here (no signing people in from another site).
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			data.Error = "Sign in from this page."
			renderLogin(w, http.StatusForbidden, data)
			return
		}
	}
	r.ParseForm()
	data.Next = safeNext(r.PostForm.Get("next"), route)
	user := strings.TrimSpace(r.PostForm.Get("user"))
	if !data.AskName {
		user = users[0]
	}
	data.User = user
	if g.limited(route.ID, client, user) {
		data.Error = "Too many attempts. Wait a few minutes and try again."
		renderLogin(w, http.StatusTooManyRequests, data)
		return
	}
	if !g.checkUser(route.ID, creds, user, r.PostForm.Get("password")) {
		g.failed(route.ID, client, user)
		data.Error = "Wrong name or password."
		if !data.AskName {
			data.Error = "Wrong password."
		}
		renderLogin(w, http.StatusUnauthorized, data)
		return
	}
	ttl := route.Protection.SessionTTL()
	value, err := g.sign(session{Route: route.ID, User: user, Expires: time.Now().Add(ttl).Unix(), Secret: fingerprint(creds.Users[user].Hash)})
	if err != nil {
		data.Error = "Sign-in is unavailable: Smart Proxy's Secret can't be read."
		renderLogin(w, http.StatusServiceUnavailable, data)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName(route.ID), Value: value, Path: cookiePath(route), MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, data.Next, http.StatusSeeOther)
}

// ServeLogout ends a browser's login.
func (g *Guard) ServeLogout(w http.ResponseWriter, r *http.Request, route store.RouteConfig) {
	http.SetCookie(w, &http.Cookie{Name: CookieName(route.ID), Value: "", Path: cookiePath(route), MaxAge: -1,
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, basePath(route)+LoginPath, http.StatusSeeOther)
}

// Deny answers a request without valid credentials: browsers go to the login page, others get
// 401 saying how to authenticate.
func Deny(w http.ResponseWriter, r *http.Request, route store.RouteConfig, page bool) {
	if page {
		http.Redirect(w, r, LoginURL(route, r), http.StatusFound)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="smart-proxy"`)
	http.Error(w, "Smart Proxy: this application requires an access token (Authorization: Bearer <token>, or "+TokenHeader+": <token>)", http.StatusUnauthorized)
}

func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

type loginPage struct {
	Host, Action, Next, User, Error string
	AskName                         bool
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>Sign in · {{.Host}}</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
.card{width:100%;max-width:380px;margin:16px;padding:32px;background:#1e293b;border:1px solid #334155;border-radius:16px;box-shadow:0 20px 40px rgba(0,0,0,.35)}
h1{margin:0 0 4px;font-size:20px}.host{margin:0 0 24px;color:#94a3b8;font-size:14px;word-break:break-all}
label{display:block;margin:0 0 6px;font-size:13px;color:#cbd5e1}
input{box-sizing:border-box;width:100%;padding:10px 12px;margin-bottom:16px;background:#0f172a;color:#e2e8f0;border:1px solid #475569;border-radius:8px;font-size:15px}
input:focus{outline:2px solid #3b82f6;border-color:transparent}
button{width:100%;padding:11px;background:#3b82f6;color:#fff;border:0;border-radius:8px;font-size:15px;font-weight:600;cursor:pointer}
button:hover{background:#2563eb}.err{color:#fecaca;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.35);border-radius:8px;padding:10px 12px;margin-bottom:16px;font-size:14px}
.foot{margin-top:20px;font-size:12px;color:#64748b;text-align:center}
</style></head><body><main class="card">
<h1>Sign in</h1><p class="host">to {{.Host}}</p>
{{if .Error}}<div class="err" role="alert">{{.Error}}</div>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="next" value="{{.Next}}">
{{if .AskName}}<label for="user">Name</label><input id="user" name="user" value="{{.User}}" autocomplete="username" autofocus required>{{end}}
<label for="password">Password</label><input id="password" type="password" name="password" autocomplete="current-password" {{if not .AskName}}autofocus{{end}} required>
<button type="submit">Sign in</button></form>
<p class="foot">Protected by Smart Proxy</p></main></body></html>`))

func renderLogin(w http.ResponseWriter, status int, data loginPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	loginTmpl.Execute(w, data)
}
