package proxy

import (
	"net/http"
	"strings"

	"smart-proxy/internal/guard"
	"smart-proxy/internal/store"
)

// authenticate checks a request to a protected route. Without the guard (Smart Proxy's Secret
// is unavailable), protected routes let nobody in rather than everybody.
func (h *Handler) authenticate(r *http.Request, route store.RouteConfig) (guard.Identity, bool) {
	if h.Guard == nil {
		return guard.Identity{}, false
	}
	return h.Guard.Authenticate(r, route)
}

// handleLogin serves the login and logout pages of a protected route.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	path := cleanPath(r.URL.Path)
	logout := strings.HasSuffix(path, guard.LogoutPath)
	prefix := strings.TrimSuffix(strings.TrimSuffix(path, guard.LoginPath), guard.LogoutPath)
	route, found := h.matchRoute(requestHost(r), cleanPath(prefix+"/"))
	if !found || route.Protection == nil || !route.Protection.Enabled {
		http.NotFound(w, r)
		return
	}
	if h.Guard == nil {
		http.Error(w, "Smart Proxy: sign-in is unavailable (Smart Proxy's Secret can't be used)", http.StatusServiceUnavailable)
		return
	}
	if logout {
		h.Guard.ServeLogout(w, r, route)
		return
	}
	h.Guard.ServeLogin(w, r, route, h.TrustedProxies.ClientIP(r))
}
