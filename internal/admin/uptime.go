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
	Days      int              `json:"days"` // Calendar days the period touches
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
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

// handleUptime reports how long each managed workload was up over a period: the last days
// (days=1..30, today included) or from=..&to=.. (RFC 3339, within the last 30 days), with days in
// tz (an IANA time zone). route=<id> or workload=<namespace>/<workload> narrow it down; hours=1
// adds each hour, changes=1 the minutes the replicas changed (both on by default for one route or
// workload).
func (s *Server) handleUptime(w http.ResponseWriter, r *http.Request) {
	if s.Uptime == nil {
		http.Error(w, "No history without Kubernetes", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	loc, err := time.LoadLocation(q.Get("tz"))
	if err != nil {
		loc = time.UTC
	}
	now := time.Now()
	from, to := uptime.LastDays(now, uptime.Days, loc)
	if days, err := strconv.Atoi(q.Get("days")); err == nil && days >= 1 && days <= uptime.Days {
		from, to = uptime.LastDays(now, days, loc)
	}
	if q.Get("from") != "" || q.Get("to") != "" {
		f, errF := time.Parse(time.RFC3339, q.Get("from"))
		t, errT := time.Parse(time.RFC3339, q.Get("to"))
		if q.Get("to") == "" {
			t, errT = now, nil
		}
		if errF != nil || errT != nil || !t.After(f) {
			http.Error(w, "from and to must be RFC 3339 times, from before to", http.StatusBadRequest)
			return
		}
		if oldest := now.Add(-uptime.Days * 24 * time.Hour).Truncate(time.Hour); f.Before(oldest) {
			f = oldest
		}
		if t.After(now) {
			t = now
		}
		from, to = f, t
	}

	managed := managedWorkloads(s.store.GetAllRoutes())
	var only map[string]bool
	if routeID := q.Get("route"); routeID != "" {
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
	if workload := q.Get("workload"); workload != "" {
		only = map[string]bool{workload: true}
	}
	opt := uptime.Options{Hours: only != nil, Changes: only != nil}
	if v := q.Get("hours"); v != "" {
		opt.Hours = v == "1" || v == "true"
	}
	if v := q.Get("changes"); v != "" {
		opt.Changes = v == "1" || v == "true"
	}

	out := UptimeReport{From: from.In(loc), To: to.In(loc), Timezone: loc.String(), Workloads: []WorkloadUptime{}}
	for _, series := range s.Uptime.Snapshot(now) {
		k := series.Namespace + "/" + series.Workload
		if only != nil && !only[k] {
			continue
		}
		rep := uptime.Summarize(series, from, to, loc, opt)
		out.Days = len(rep.Days)
		if rep.Observed == 0 && len(rep.Changes) == 0 {
			continue
		}
		if rep.Since != nil && (out.Since == nil || rep.Since.Before(*out.Since)) {
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
