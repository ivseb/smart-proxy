package proxy

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// target is where a request is proxied, and why there.
type target struct {
	Service string
	Port    int
	Why     string // For the inspector: "split", "sticky"... ("" for a single target)
}

// backendServing reports whether a backend can take traffic now: its workload has a ready
// replica. A backend whose workload is unknown is assumed to serve, as the router would try it.
func (h *Handler) backendServing(namespace string, b store.WeightedBackend) bool {
	if b.Workload == "" {
		return true
	}
	replicas, ready, err := h.k8sClient.GetDeploymentStatus(namespace, b.Workload)
	if err != nil {
		return true
	}
	return replicas > 0 && ready > 0
}

// servingBackends are the route's backends that can take traffic now (weight above zero).
func (h *Handler) servingBackends(route store.RouteConfig) []store.WeightedBackend {
	var serving []store.WeightedBackend
	for _, b := range route.Backends {
		if b.Weight > 0 && h.backendServing(route.Namespace, b) {
			serving = append(serving, b)
		}
	}
	return serving
}

// hasServingPassThrough reports whether a backend Smart Proxy doesn't manage is running, so it
// can take the traffic while the managed ones wake up.
func (h *Handler) hasServingPassThrough(route store.RouteConfig) bool {
	for _, b := range h.servingBackends(route) {
		if !b.Managed {
			return true
		}
	}
	return false
}

// Why a backend was chosen, as shown by the inspector.
const (
	WhySplit     = "split"     // Weighted choice
	WhySticky    = "sticky"    // The client's earlier choice (cookie)
	WhyCondition = "condition" // The request matched one of the backend's conditions
	WhyPinned    = "pinned"    // Kept on a backend taking no share of traffic, chosen by a condition or the link
)

// pinFor is how long a client stays on a backend chosen by a condition or the link.
const pinFor = 12 * time.Hour

// explicitBackend returns the backend a request goes to whatever the weights: the first whose
// conditions it matches, or one taking no share of traffic the client was pinned to.
func (h *Handler) explicitBackend(r *http.Request, client net.IP, route store.RouteConfig) (*store.WeightedBackend, string) {
	for i := range route.Backends {
		b := &route.Backends[i]
		for _, c := range b.When {
			if matchCondition(c, r, client) {
				return b, WhyCondition
			}
		}
	}
	if c, err := r.Cookie(stickyCookie(route.ID)); err == nil {
		for i := range route.Backends {
			if b := &route.Backends[i]; b.Service == c.Value && b.Weight == 0 {
				return b, WhyPinned
			}
		}
	}
	return nil, ""
}

// matchCondition reports whether a request matches a backend condition. Text comparisons
// ignore case, as people write header values and paths both ways.
func matchCondition(c store.Condition, r *http.Request, client net.IP) bool {
	var value string
	var present bool
	switch c.Field {
	case store.FieldHeader:
		values := r.Header.Values(c.Name)
		value, present = strings.Join(values, ", "), len(values) > 0
	case store.FieldCookie:
		if ck, err := r.Cookie(c.Name); err == nil {
			value, present = ck.Value, true
		}
	case store.FieldQuery:
		value, present = r.URL.Query().Get(c.Name), r.URL.Query().Has(c.Name)
	case store.FieldPath:
		value, present = cleanPath(r.URL.Path), true
	case store.FieldClient:
		nets, err := traffic.ParseTrustedProxies([]string{c.Value})
		if err != nil || client == nil {
			return false
		}
		for _, n := range nets {
			if n.Contains(client) {
				return true
			}
		}
		return false
	}
	if !present {
		return false
	}
	got, want := strings.ToLower(strings.TrimSpace(value)), strings.ToLower(strings.TrimSpace(c.Value))
	switch c.Op {
	case store.OpExists:
		return true
	case store.OpEquals:
		return got == want
	case store.OpContains:
		return strings.Contains(got, want)
	case store.OpPrefix:
		return strings.HasPrefix(got, want)
	}
	return false
}

// pin keeps a client on a backend. Over HTTPS the cookie is SameSite=None, so it also comes with
// cross-site form posts, such as a payment or sign-in page posting back.
func pin(w http.ResponseWriter, r *http.Request, route store.RouteConfig, service string, maxAge time.Duration) {
	cookie := &http.Cookie{Name: stickyCookie(route.ID), Value: service, Path: cookiePath(route), HttpOnly: true, MaxAge: int(maxAge.Seconds())}
	if maxAge < 0 {
		cookie.MaxAge = -1
	}
	if isHTTPS(r) {
		cookie.Secure, cookie.SameSite = true, http.SameSiteNoneMode
	} else {
		cookie.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, cookie)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// cookiePath scopes a route's cookies ("/app" covers "/app" and "/app/...").
func cookiePath(route store.RouteConfig) string {
	if p := strings.TrimSuffix(route.Path, "/"); p != "" {
		return p
	}
	return "/"
}

// routeTo is the route as seen by a request sent to one of its backends: that backend is its
// only target, and its workload the one woken.
func routeTo(route store.RouteConfig, b store.WeightedBackend) store.RouteConfig {
	view := route
	view.Backends = nil
	view.TargetService, view.TargetPort = b.Service, b.Port
	if b.Workload != "" {
		view.Deployment = b.Workload
	}
	return view
}

// UsePath, under a route's path, pins the client to one of its backends ("default" unpins):
// the link testers open to try a backend.
const UsePath = "/__smart_proxy/use/"

// handleUse serves the backend link.
func (h *Handler) handleUse(w http.ResponseWriter, r *http.Request) {
	prefix, name, _ := strings.Cut(r.URL.Path, UsePath)
	route, found := h.matchRoute(requestHost(r), cleanPath(prefix+"/"))
	if !found {
		http.NotFound(w, r)
		return
	}
	target := cookiePath(route)
	if name == "default" {
		pin(w, r, route, "", -1)
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	for _, b := range route.Backends {
		if b.Service == name {
			pin(w, r, route, b.Service, pinFor)
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
	}
	http.Error(w, "Smart Proxy: this route has no backend "+name, http.StatusNotFound)
}

// stickyCookie keeps a client on the backend it was given, as the OpenShift router does.
func stickyCookie(routeID string) string {
	h := fnv.New32a()
	h.Write([]byte(routeID))
	return fmt.Sprintf("sp_backend_%08x", h.Sum32())
}

// pickTarget chooses the Service for a request: the route's single target, or one of its
// serving backends, by weight, sticking to the backend chosen before for this client.
func (h *Handler) pickTarget(w http.ResponseWriter, r *http.Request, route store.RouteConfig) target {
	if len(route.Backends) == 0 {
		return target{route.TargetService, route.TargetPort, ""}
	}
	serving := h.servingBackends(route)
	if len(serving) == 0 {
		return target{route.TargetService, route.TargetPort, ""}
	}

	name := stickyCookie(route.ID)
	if c, err := r.Cookie(name); err == nil {
		for _, b := range serving {
			if b.Service == c.Value {
				return target{b.Service, b.Port, WhySticky}
			}
		}
	}

	var total int64
	for _, b := range serving {
		total += int64(b.Weight)
	}
	chosen := serving[0]
	n := rand.Int64N(total)
	for _, b := range serving {
		if n < int64(b.Weight) {
			chosen = b
			break
		}
		n -= int64(b.Weight)
	}
	// While a managed backend wakes up, its share goes elsewhere for now: don't stick to that.
	for _, b := range route.Backends {
		if b.Managed && b.Weight > 0 && !h.backendServing(route.Namespace, b) {
			return target{chosen.Service, chosen.Port, WhySplit}
		}
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: name, Value: chosen.Service, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	return target{chosen.Service, chosen.Port, WhySplit}
}
