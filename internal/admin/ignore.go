package admin

import (
	"net/http"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// IgnoreRule is one rule keeping requests from counting as activity, with what it matched over
// the last 24 hours.
type IgnoreRule struct {
	Kind string     `json:"kind"` // user_agents, paths, sources, methods, or probes
	Rule string     `json:"rule"`
	Hits int64      `json:"hits"`
	Last *time.Time `json:"last,omitempty"`
}

// IgnoreRules are the rules applying to a route: its own, the global ones (Helm values and the
// built-in monitors), and its workloads' Kubernetes probe paths.
type IgnoreRules struct {
	Route  []IgnoreRule `json:"route"`
	Global []IgnoreRule `json:"global"`
	Probes []IgnoreRule `json:"probes"`
}

// kinds maps the reasons a request is ignored to the lists of traffic.Rules (as in its JSON).
var kinds = map[string]string{
	traffic.ReasonUserAgent: "user_agents",
	traffic.ReasonPath:      "paths",
	traffic.ReasonSource:    "sources",
	traffic.ReasonMethod:    "methods",
	traffic.ReasonProbePath: "probes",
}

func ruleLists(r traffic.Rules) map[string][]string {
	return map[string][]string{"user_agents": r.UserAgents, "paths": r.Paths, "sources": r.Sources, "methods": r.Methods}
}

// handleIgnoreRules lists the ignore rules of a route with how often each matched.
func (s *Server) handleIgnoreRules(w http.ResponseWriter, r *http.Request) {
	route, ok := s.store.GetRoute(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	hits := map[string]traffic.RuleHit{}
	if s.RuleHits != nil {
		for _, h := range s.RuleHits(route.ID) {
			hits[kinds[h.Reason]+" "+h.Rule] = h
		}
	}
	now := time.Now()
	view := func(kind, rule string) IgnoreRule {
		v := IgnoreRule{Kind: kind, Rule: rule}
		if h, ok := hits[kind+" "+rule]; ok {
			v.Hits = h.Total(now)
			last := h.Last
			v.Last = &last
		}
		return v
	}
	out := IgnoreRules{Route: []IgnoreRule{}, Global: []IgnoreRule{}, Probes: []IgnoreRule{}}
	order := []string{"paths", "user_agents", "sources", "methods"}
	if route.Ignore != nil {
		lists := ruleLists(*route.Ignore)
		for _, kind := range order {
			for _, rule := range lists[kind] {
				out.Route = append(out.Route, view(kind, rule))
			}
		}
	}
	// A global rule the route repeats is listed once, as the route's (its matches are counted there).
	own := map[string]bool{}
	for _, r := range out.Route {
		own[r.Kind+" "+r.Rule] = true
	}
	global := ruleLists(s.GlobalRules)
	for _, kind := range order {
		for _, rule := range global[kind] {
			if !own[kind+" "+rule] {
				out.Global = append(out.Global, view(kind, rule))
			}
		}
	}
	if s.k8sClient != nil {
		seen := map[string]bool{}
		for _, workload := range append([]string{route.Deployment}, dependencyNames(route.Dependencies)...) {
			paths, _ := s.k8sClient.GetDeploymentProbePaths(route.Namespace, workload)
			for _, p := range paths {
				if !seen[p] {
					seen[p] = true
					out.Probes = append(out.Probes, view("probes", p))
				}
			}
		}
	}
	writeJSON(w, out)
}

func dependencyNames(deps []store.DependencyConfig) []string {
	names := make([]string, 0, len(deps))
	for _, d := range deps {
		names = append(names, d.Name)
	}
	return names
}
