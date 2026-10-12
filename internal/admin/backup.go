package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
	"smart-proxy/internal/trash"
)

// ExportKind identifies a file of exported routes.
const ExportKind = "smart-proxy/routes"

// Export is a file of routes, as downloaded from the dashboard and imported back. People and
// access tokens of protected routes are not in it: they are added again after an import.
type Export struct {
	Kind       string              `json:"kind"`
	Version    int                 `json:"version"`
	ExportedAt time.Time           `json:"exported_at"`
	ExportedBy string              `json:"exported_by,omitempty"`
	Routes     []store.RouteConfig `json:"routes"`
}

// portable is a route as exported: configuration only, without runtime state.
func portable(r store.RouteConfig) store.RouteConfig {
	r.LastActivity, r.InspectUntil, r.Pause, r.Declarative = time.Time{}, nil, nil, false
	return r
}

// handleExport downloads routes (ids=a,b; all of them when absent). Routes defined by
// annotations live in Git already and are left out.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	want := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	out := Export{Kind: ExportKind, Version: 1, ExportedAt: time.Now().UTC(), ExportedBy: auth.UserFromContext(r.Context()), Routes: []store.RouteConfig{}}
	for _, route := range s.store.GetAllRoutes() {
		if route.Declarative || (len(want) > 0 && !want[route.ID]) {
			continue
		}
		out.Routes = append(out.Routes, portable(route))
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].ID < out.Routes[j].ID })
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="smart-proxy-routes-%s.json"`, time.Now().Format("2006-01-02")))
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// ImportItem is what importing one route would do, or did.
type ImportItem struct {
	ID         string   `json:"id"`
	Host       string   `json:"host"`
	Path       string   `json:"path"`
	Namespace  string   `json:"namespace"`
	Deployment string   `json:"deployment"`
	Status     string   `json:"status"`            // new, changed, same, invalid
	Changes    []string `json:"changes,omitempty"` // Settings that differ from the current route
	Problems   []string `json:"problems,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Result     string   `json:"result,omitempty"` // After applying: imported, or the error
}

const maxImport = 2 << 20

// readImport accepts an export file, or a bare list of routes.
func readImport(r *http.Request) ([]store.RouteConfig, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxImport+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImport {
		return nil, fmt.Errorf("the file is larger than %d MiB", maxImport>>20)
	}
	var file Export
	if err := json.Unmarshal(data, &file); err == nil && file.Kind != "" {
		if file.Kind != ExportKind {
			return nil, fmt.Errorf("not a file of Smart Proxy routes (kind %q)", file.Kind)
		}
		return file.Routes, nil
	}
	var routes []store.RouteConfig
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, fmt.Errorf("not a file of Smart Proxy routes: %v", err)
	}
	return routes, nil
}

// handleImport previews (POST) or applies (POST apply=true, ids=a,b) importing routes. A route
// replaces the one with its ID; its Ingresses/Routes are patched as when it is saved.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	routes, err := readImport(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apply := r.URL.Query().Get("apply") == "true"
	selected := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id != "" {
			selected[id] = true
		}
	}
	items := make([]ImportItem, 0, len(routes))
	for i := range routes {
		route := portable(routes[i])
		item := s.previewImport(route, routes[:i])
		if apply && (selected[item.ID] || (len(selected) == 0 && (item.Status == "new" || item.Status == "changed"))) {
			if item.Status == "invalid" {
				item.Result = strings.Join(item.Problems, "; ")
			} else {
				if existing, ok := s.store.GetRoute(route.ID); ok && route.ID != "" {
					route.InspectUntil, route.Pause = existing.InspectUntil, existing.Pause
				}
				if _, err := s.saveRoute(&route); err != nil {
					item.Result = err.Error()
				} else {
					item.Result = "imported"
					logger.Printf("Route %s imported by %s", route.ID, orSomeone(auth.UserFromContext(r.Context())))
				}
			}
		}
		items = append(items, item)
	}
	writeJSON(w, items)
}

// previewImport says what importing a route would do; earlier are the routes before it in the
// file.
func (s *Server) previewImport(route store.RouteConfig, earlier []store.RouteConfig) ImportItem {
	item := ImportItem{ID: route.ID, Host: route.Host, Path: route.Path, Namespace: route.Namespace, Deployment: route.Deployment, Status: "new"}
	if item.ID == "" {
		item.ID = "(new) " + route.Namespace + "/" + route.Deployment
	}
	problem := func(format string, args ...any) { item.Problems = append(item.Problems, fmt.Sprintf(format, args...)) }
	if route.Namespace == "" || route.Deployment == "" {
		problem("namespace and workload are required")
	}
	if err := route.Protection.Validate(); err != nil {
		problem("protection: %v", err)
	}
	if err := route.ValidateTraffic(); err != nil {
		problem("traffic settings: %v", err)
	}
	if route.Schedule != nil {
		if err := route.Schedule.Validate(); err != nil {
			problem("schedule: %v", err)
		}
	}
	check := route
	if err := check.NormalizeBackends(); err != nil {
		problem("backends: %v", err)
	}
	if s.k8sClient != nil && route.Namespace != "" && !s.k8sClient.Watches(route.Namespace) {
		problem("namespace %s is not managed by this Smart Proxy", route.Namespace)
	}
	for _, other := range earlier {
		if other.Namespace == route.Namespace && other.Host == route.Host && samePath(other.Path, route.Path) {
			problem("the file has another route for the same host and path (%s)", other.ID)
			break
		}
	}
	for _, other := range s.store.GetAllRoutes() {
		if other.ID != route.ID && other.Namespace == route.Namespace && other.Host == route.Host && samePath(other.Path, route.Path) {
			problem("route %s already serves this host and path", other.ID)
		}
	}

	if existing, ok := s.store.GetRoute(route.ID); ok && route.ID != "" {
		if existing.Declarative {
			problem("the current route is defined by smart-proxy/* annotations: change them instead")
		}
		item.Changes = changedSettings(portable(*existing), route)
		item.Status = "changed"
		if len(item.Changes) == 0 {
			item.Status = "same"
		}
	}
	if len(item.Problems) > 0 {
		item.Status = "invalid"
		return item
	}
	if s.k8sClient != nil && route.Deployment != "" {
		if _, _, err := s.k8sClient.GetDeploymentStatus(route.Namespace, route.Deployment); err != nil {
			item.Warnings = append(item.Warnings, fmt.Sprintf("workload %s not found in %s: the route waits for it", route.Deployment, route.Namespace))
		}
	}
	if route.Protection != nil && route.Protection.Enabled {
		users, tokens := s.credentials(route.ID)
		if len(users) == 0 && len(tokens) == 0 {
			item.Warnings = append(item.Warnings, "protected: add people or access tokens after importing, until then nobody gets in")
		}
	}
	return item
}

// changedSettings names the settings (JSON fields) that differ between two routes.
func changedSettings(a, b store.RouteConfig) []string {
	var ma, mb map[string]any
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	json.Unmarshal(ja, &ma)
	json.Unmarshal(jb, &mb)
	keys := map[string]bool{}
	for k := range ma {
		keys[k] = true
	}
	for k := range mb {
		keys[k] = true
	}
	var changed []string
	for k := range keys {
		if !reflect.DeepEqual(ma[k], mb[k]) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return changed
}

// DeletedRoute is a route in the trash, as listed in the dashboard.
type DeletedRoute struct {
	ID          string    `json:"id"`
	RouteID     string    `json:"route_id"`
	Host        string    `json:"host"`
	Path        string    `json:"path"`
	Namespace   string    `json:"namespace"`
	Deployment  string    `json:"deployment"`
	DeletedAt   time.Time `json:"deleted_at"`
	DeletedBy   string    `json:"deleted_by,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	Resources   []string  `json:"resources,omitempty"`
	Credentials bool      `json:"credentials,omitempty"`
	Conflict    bool      `json:"conflict,omitempty"` // A route with its ID exists again
}

// handleDeleted lists the deleted routes (GET), restores one (POST id=) or removes it for good
// (DELETE id=).
func (s *Server) handleDeleted(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := r.URL.Query().Get("id")
	switch r.Method {
	case http.MethodGet:
		entries, expired, err := s.Trash.List(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.forgetExpired(expired)
		out := make([]DeletedRoute, 0, len(entries))
		for _, e := range entries {
			_, exists := s.store.GetRoute(e.Route.ID)
			out = append(out, DeletedRoute{ID: e.ID, RouteID: e.Route.ID, Host: e.Route.Host, Path: e.Route.Path, Namespace: e.Route.Namespace,
				Deployment: e.Route.Deployment, DeletedAt: e.DeletedAt, DeletedBy: e.DeletedBy, ExpiresAt: e.DeletedAt.Add(trash.Retention),
				Resources: e.Resources, Credentials: e.Credentials, Conflict: exists})
		}
		writeJSON(w, out)

	case http.MethodPost:
		entries, _, err := s.Trash.List(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var found bool
		for _, e := range entries {
			if e.ID == id {
				found = true
				if _, exists := s.store.GetRoute(e.Route.ID); exists {
					http.Error(w, "A route with the same ID exists again: delete it first, or import this one over it", http.StatusConflict)
					return
				}
			}
		}
		if !found {
			http.Error(w, "Not in the trash (restored or expired)", http.StatusNotFound)
			return
		}
		entry, ok, err := s.Trash.Take(ctx, id)
		if err != nil || !ok {
			http.Error(w, "Not in the trash anymore", http.StatusConflict)
			return
		}
		route := entry.Route
		if entry.Credentials {
			s.moveCredentials(entry.CredentialsKey(), route.ID)
		}
		if status, err := s.saveRoute(&route); err != nil {
			// Back in the trash, credentials included, so nothing is lost.
			if entry.Credentials {
				s.moveCredentials(route.ID, entry.CredentialsKey())
			}
			if _, addErr := s.Trash.Add(ctx, entry); addErr != nil {
				logger.Printf("Warning: route %s couldn't be restored nor put back in the trash: %v", route.ID, addErr)
			}
			http.Error(w, err.Error(), status)
			return
		}
		logger.Printf("Route %s restored from the trash by %s", route.ID, orSomeone(auth.UserFromContext(r.Context())))
		writeJSON(w, map[string]any{"id": route.ID})

	case http.MethodDelete:
		entry, ok, err := s.Trash.Take(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok && entry.Credentials {
			s.forgetCredentials(entry.CredentialsKey())
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
