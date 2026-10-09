package proxy

import (
	"net"
	"net/http"
	"path"
	"strings"

	"smart-proxy/internal/store"
)

// requestHost is the request's host name: without port or trailing dot.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

// matchPath reports whether a request path falls under a route's path. Ingress paths match
// whole segments ("/api" serves "/api" and "/api/x", not "/apidocs"); OpenShift Routes match
// plain prefixes, as the OpenShift router does.
func matchPath(route store.RouteConfig, path string) bool {
	prefix := route.Path
	if prefix == "" || prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if strings.HasPrefix(route.ID, "route-") || strings.HasSuffix(prefix, "/") || len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}

// cleanPath resolves "." and ".." segments and duplicate slashes, keeping a trailing slash.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	cleaned := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// matchRoute finds the route serving a host and path: the longest matching path, and a route
// naming the host over a catch-all one.
func (h *Handler) matchRoute(host, path string) (store.RouteConfig, bool) {
	var best store.RouteConfig
	found := false
	for _, route := range h.store.GetAllRoutes() {
		if !matchHost(route.Host, host) || !matchPath(route, path) {
			continue
		}
		if !found || len(route.Path) > len(best.Path) ||
			(len(route.Path) == len(best.Path) && route.Host != "" && best.Host == "") {
			best, found = route, true
		}
	}
	return best, found
}

// matchHost checks if the requestHost matches a comma-separated list of route hosts (case-insensitive)
func matchHost(routeHost, requestHost string) bool {
	if routeHost == "" {
		return true
	}
	for _, part := range strings.Split(routeHost, ",") {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(part), "."), requestHost) {
			return true
		}
	}
	return false
}

// wantsPage reports whether a request is a browser navigation, which can be answered with the
// "waking up" page. API calls, form posts, WebSockets and the like wait for the app instead.
func wantsPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
