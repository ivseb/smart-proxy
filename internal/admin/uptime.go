package admin

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/uptime"
)

// RouteRef names a route a workload serves.
type RouteRef struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Path string `json:"path"`
}

// WorkloadUptime is a workload's history over the period asked, with the routes it serves now
// (none: no longer managed).
type WorkloadUptime struct {
	uptime.Report
	Routes []RouteRef `json:"routes"`
}

// UptimeReport is what /api/uptime answers.
type UptimeReport struct {
	Days      int              `json:"days"`
	Timezone  string           `json:"timezone"`
	Since     *time.Time       `json:"since,omitempty"` // First hour recorded, in the period
	Workloads []WorkloadUptime `json:"workloads"`
}

// managedWorkloads maps each workload the routes manage (namespace/canonical ref) to its routes.
func managedWorkloads(routes []store.RouteConfig) map[string][]RouteRef {
	out := map[string][]RouteRef{}
	for _, r := range routes {
		refs := r.ManagedWorkloads()
		for _, d := range r.Dependencies {
			refs = append(refs, d.Name)
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			k := r.Namespace + "/" + uptime.Canonical(ref)
			if !seen[k] {
				seen[k] = true
				out[k] = append(out[k], RouteRef{ID: r.ID, Host: r.Host, Path: r.Path})
			}
		}
	}
	return out
}

// handleUptime reports how long each managed workload was up over the last days (days=1..30,
// tz=IANA time zone for the days, route=<id> for one route's workloads, hour by hour too).
func (s *Server) handleUptime(w http.ResponseWriter, r *http.Request) {
	if s.Uptime == nil {
		http.Error(w, "No history without Kubernetes", http.StatusServiceUnavailable)
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 || days > uptime.Days {
		days = uptime.Days
	}
	loc, err := time.LoadLocation(r.URL.Query().Get("tz"))
	if err != nil {
		loc = time.UTC
	}
	routeID := r.URL.Query().Get("route")
	managed := managedWorkloads(s.store.GetAllRoutes())
	var only map[string]bool
	if routeID != "" {
		route, ok := s.store.GetRoute(routeID)
		if !ok {
			http.Error(w, "Route not found", http.StatusNotFound)
			return
		}
		only = map[string]bool{}
		for k := range managedWorkloads([]store.RouteConfig{*route}) {
			only[k] = true
		}
	}

	now := time.Now()
	out := UptimeReport{Days: days, Timezone: loc.String(), Workloads: []WorkloadUptime{}}
	for _, series := range s.Uptime.Snapshot(now) {
		k := series.Namespace + "/" + series.Workload
		if only != nil && !only[k] {
			continue
		}
		rep := uptime.Summarize(series, now, days, loc, only != nil)
		if rep.Observed == 0 {
			continue
		}
		if out.Since == nil || rep.Since.Before(*out.Since) {
			out.Since = rep.Since
		}
		routes := managed[k]
		if routes == nil {
			routes = []RouteRef{}
		}
		out.Workloads = append(out.Workloads, WorkloadUptime{Report: rep, Routes: routes})
	}
	sort.Slice(out.Workloads, func(i, j int) bool {
		a, b := out.Workloads[i], out.Workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Workload < b.Workload
	})
	writeJSON(w, out)
}
