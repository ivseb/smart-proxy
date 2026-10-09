package proxy

import (
	"net"
	"net/http"
	"path"
	"sort"
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

// matchPath reports whether a request path falls under a route's path, on whole segments as
// Ingress Prefix paths and the OpenShift router do: "/api" and "/api/" serve "/api" and
// "/api/x", not "/apidocs". An Exact Ingress path serves only itself.
func matchPath(route store.RouteConfig, path string) bool {
	prefix := route.Path
	if route.PathExact {
		return path == prefix
	}
	if prefix == "" || prefix == "/" {
		return true
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// cleanPath is the path decisions are made on (routes, protection): "." and ".." resolved,
// duplicate slashes merged, a trailing slash kept. Matrix parameters (";jsessionid=…") are
// dropped from each segment and backslashes read as slashes, as Java servers and some proxies
// do: otherwise "/open/..;/admin" would look open here and reach /admin there.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.Contains(p, ";") {
		segments := strings.Split(p, "/")
		for i, seg := range segments {
			if j := strings.IndexByte(seg, ';'); j >= 0 {
				segments[i] = seg[:j]
			}
		}
		p = strings.Join(segments, "/")
	}
	cleaned := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// routeIndex finds routes by host without scanning (or copying) every route per request. It is
// rebuilt when the configurations change.
type routeIndex struct {
	version  uint64
	exact    map[string][]store.RouteConfig // Lower-case host -> routes, longest path first
	wildcard map[string][]store.RouteConfig // "*.example.com" -> routes, by "example.com"
	catchAll []store.RouteConfig            // Routes without a host
}

func buildIndex(version uint64, routes []store.RouteConfig) *routeIndex {
	idx := &routeIndex{version: version, exact: map[string][]store.RouteConfig{}, wildcard: map[string][]store.RouteConfig{}}
	for _, route := range routes {
		hosts := splitHosts(route.Host)
		if len(hosts) == 0 {
			idx.catchAll = append(idx.catchAll, route)
		}
		for _, h := range hosts {
			if domain, ok := strings.CutPrefix(h, "*."); ok {
				idx.wildcard[domain] = append(idx.wildcard[domain], route)
			} else {
				idx.exact[h] = append(idx.exact[h], route)
			}
		}
	}
	byPath := func(list []store.RouteConfig) {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i].Path) > len(list[j].Path) })
	}
	for _, list := range idx.exact {
		byPath(list)
	}
	for _, list := range idx.wildcard {
		byPath(list)
	}
	byPath(idx.catchAll)
	return idx
}

func splitHosts(value string) []string {
	var hosts []string
	for _, part := range strings.Split(value, ",") {
		if h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(part), ".")); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// matchRoute finds the route serving a host and path. As with ingress controllers, the host
// decides first (an exact host, then a wildcard one, then routes without a host); among the
// routes of that host, the longest matching path wins.
func (h *Handler) matchRoute(host, path string) (store.RouteConfig, bool) {
	idx := h.index.Load()
	if version := h.store.Version(); idx == nil || idx.version != version {
		idx = buildIndex(version, h.store.GetAllRoutes())
		h.index.Store(idx)
	}
	host = strings.ToLower(host)
	candidates := [][]store.RouteConfig{idx.exact[host]}
	if _, domain, ok := strings.Cut(host, "."); ok {
		candidates = append(candidates, idx.wildcard[domain]) // One label, as Ingress wildcards
	}
	candidates = append(candidates, idx.catchAll)
	for _, list := range candidates {
		for _, route := range list {
			if matchPath(route, path) {
				return route, true
			}
		}
		// Some controllers match plain prefixes (Traefik by default, ImplementationSpecific
		// paths): whatever they sent for this host belongs to its longest prefix.
		for _, route := range list {
			if !route.PathExact && strings.HasPrefix(path, route.Path) {
				return route, true
			}
		}
	}
	return store.RouteConfig{}, false
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
