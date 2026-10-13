package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"smart-proxy/internal/k8s"
	"smart-proxy/internal/k8s/fakecluster"
	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
	"smart-proxy/internal/uptime"
	"smart-proxy/internal/vault"
)

// patchWeb patches team-a/web and returns its route ID.
func patchWeb(t *testing.T, f *fixture) string {
	t.Helper()
	if code, body := f.call(t, "POST", "/api/patch-ingress?namespace=team-a&name=web", nil); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	f.waitCached(t, "team-a", "web", true)
	return store.IngressID("team-a", "web")
}

func withVault(t *testing.T, f *fixture) *vault.Vault {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v := &vault.Vault{Client: f.cluster.Kube, Namespace: "sp", Name: "sp-state"}
	if err := v.Start(ctx); err != nil {
		t.Fatal(err)
	}
	f.server.Vault = func() *vault.Vault { return v }
	return v
}

func TestPauseKeepsTheRouteAwakeUntilItEnds(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 1)...)
	id := patchWeb(t, f)

	if code, _ := f.call(t, "POST", "/api/routes/pause?id="+url.QueryEscape(id)+"&until=2001-01-01T00:00:00Z", nil); code != http.StatusBadRequest {
		t.Fatalf("pause in the past: %d", code)
	}
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if code, body := f.call(t, "POST", "/api/routes/pause?id="+url.QueryEscape(id)+"&until="+until.Format(time.RFC3339), nil); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	route, _ := f.store.GetRoute(id)
	if !route.Paused(time.Now()) || !route.Pause.Until.Equal(until) || route.Paused(until.Add(time.Second)) {
		t.Fatalf("pause = %+v", route.Pause)
	}
	// Idle long ago: still not idle while paused, and not right after the pause ends either.
	route.LastActivity = time.Now().Add(-48 * time.Hour)
	if route.Idle(time.Now()) || route.Idle(until.Add(time.Minute)) || !route.Idle(until.Add(route.EffectiveIdleTimeout()+time.Minute)) {
		t.Fatal("pause doesn't count as activity until it ends")
	}
	var routes []RouteStatus
	_, body := f.call(t, "GET", "/api/routes", nil)
	json.Unmarshal(body, &routes)
	if routes[0].SleepsAt != nil || routes[0].Pause == nil {
		t.Fatalf("listed as sleeping soon while paused: %+v", routes[0])
	}

	// Saving the route from a form (with an old or no pause) doesn't change the pause.
	edit := *route
	edit.Pause = nil
	edit.IdleTimeout = 2 * time.Hour
	if code, body := f.call(t, "POST", "/api/routes", edit); code != http.StatusCreated {
		t.Fatalf("save: %d %s", code, body)
	}
	if r, _ := f.store.GetRoute(id); r.Pause == nil || r.IdleTimeout != 2*time.Hour {
		t.Fatalf("after save: %+v", r.Pause)
	}

	if code, _ := f.call(t, "DELETE", "/api/routes/pause?id="+url.QueryEscape(id), nil); code != 200 {
		t.Fatalf("resume: %d", code)
	}
	if r, _ := f.store.GetRoute(id); r.Pause != nil {
		t.Fatal("still paused")
	}
	// Until resumed.
	f.call(t, "POST", "/api/routes/pause?id="+url.QueryEscape(id), nil)
	if r, _ := f.store.GetRoute(id); !r.Paused(time.Now().Add(1000 * time.Hour)) {
		t.Fatal("open-ended pause ended")
	}
}

func TestDeletedRouteIsRestoredWithItsCredentials(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 1)...)
	v := withVault(t, f)
	id := patchWeb(t, f)
	route, _ := f.store.GetRoute(id)
	route.IdleTimeout = 45 * time.Minute
	route.Ignore = &traffic.Rules{Paths: []string{"/health*"}}
	if code, body := f.call(t, "POST", "/api/routes", route); code != http.StatusCreated {
		t.Fatalf("save: %d %s", code, body)
	}
	if code, body := f.call(t, "POST", "/api/routes/protection/users", map[string]string{"id": id, "name": "ops", "password": "long-enough-1"}); code >= 300 {
		t.Fatalf("user: %d %s", code, body)
	}

	if code, body := f.call(t, "DELETE", "/api/routes?id="+url.QueryEscape(id), nil); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	f.waitCached(t, "team-a", "web", false)
	if len(v.Credentials(id).Users) != 0 {
		t.Fatal("credentials left on the deleted route's ID")
	}
	var deleted []DeletedRoute
	_, body := f.call(t, "GET", "/api/routes/deleted", nil)
	json.Unmarshal(body, &deleted)
	if len(deleted) != 1 || deleted[0].RouteID != id || !deleted[0].Credentials || len(deleted[0].Resources) != 1 || deleted[0].Conflict {
		t.Fatalf("trash: %s", body)
	}

	if code, body := f.call(t, "POST", "/api/routes/deleted?id="+url.QueryEscape(deleted[0].ID), nil); code != 200 {
		t.Fatalf("restore: %d %s", code, body)
	}
	restored, ok := f.store.GetRoute(id)
	if !ok || restored.IdleTimeout != 45*time.Minute || restored.Ignore == nil || restored.Ignore.Paths[0] != "/health*" {
		t.Fatalf("restored: %+v", restored)
	}
	if !k8s.IsIngressPatched(f.ingress(t, "team-a", "web"), proxyService) {
		t.Fatal("ingress not patched again")
	}
	if _, ok := v.Credentials(id).Users["ops"]; !ok {
		t.Fatal("credentials not restored")
	}
	if code, _ := f.call(t, "POST", "/api/routes/deleted?id="+url.QueryEscape(deleted[0].ID), nil); code != http.StatusNotFound {
		t.Fatalf("restored twice: %d", code)
	}
}

func TestExportAndImportRoutes(t *testing.T) {
	f := newFixture(t, append(app("team-a", "web", 1), app("team-a", "api", 1)...)...)
	id := patchWeb(t, f)
	if code, body := f.call(t, "POST", "/api/patch-ingress?namespace=team-a&name=api", nil); code != 200 {
		t.Fatalf("patch api: %d %s", code, body)
	}
	f.waitCached(t, "team-a", "api", true)

	code, body := f.call(t, "GET", "/api/routes/export?ids="+url.QueryEscape(id), nil)
	var file Export
	if err := json.Unmarshal(body, &file); err != nil || code != 200 || file.Kind != ExportKind || len(file.Routes) != 1 || file.Routes[0].ID != id {
		t.Fatalf("export: %d %s", code, body)
	}
	if !file.Routes[0].LastActivity.IsZero() {
		t.Fatal("runtime state exported")
	}

	// Unchanged: nothing to do.
	var items []ImportItem
	_, body = f.call(t, "POST", "/api/routes/import", file)
	items = nil
	json.Unmarshal(body, &items)
	if len(items) != 1 || items[0].Status != "same" {
		t.Fatalf("preview of an unchanged route: %s", body)
	}

	// Deleted, then imported back.
	f.call(t, "DELETE", "/api/routes?id="+url.QueryEscape(id), nil)
	f.waitCached(t, "team-a", "web", false)
	file.Routes[0].IdleTimeout = 90 * time.Minute
	_, body = f.call(t, "POST", "/api/routes/import", file)
	items = nil
	json.Unmarshal(body, &items)
	if items[0].Status != "new" || len(items[0].Problems) > 0 {
		t.Fatalf("preview: %s", body)
	}
	_, body = f.call(t, "POST", "/api/routes/import?apply=true&ids="+url.QueryEscape(id), file)
	items = nil
	json.Unmarshal(body, &items)
	if items[0].Result != "imported" {
		t.Fatalf("import: %s", body)
	}
	if r, ok := f.store.GetRoute(id); !ok || r.IdleTimeout != 90*time.Minute {
		t.Fatalf("imported route: %+v", r)
	}
	fakecluster.Eventually(t, func() bool { return k8s.IsIngressPatched(f.ingress(t, "team-a", "web"), proxyService) }, "ingress not patched")

	// Changed settings are named; a conflicting or invalid route is refused.
	file.Routes[0].IdleTimeout = time.Hour
	other := file.Routes[0]
	other.ID = "manual-copy"
	broken := store.RouteConfig{ID: "broken", Namespace: "kube-system", Deployment: "x"}
	file.Routes = append(file.Routes, other, broken)
	_, body = f.call(t, "POST", "/api/routes/import", file)
	items = nil
	json.Unmarshal(body, &items)
	if items[0].Status != "changed" || len(items[0].Changes) != 1 || items[0].Changes[0] != "idle_timeout" {
		t.Fatalf("changed: %+v", items[0])
	}
	if items[1].Status != "invalid" || items[2].Status != "invalid" {
		t.Fatalf("conflict/invalid: %s", body)
	}
	if code, _ := f.call(t, "POST", "/api/routes/import", map[string]string{"kind": "something-else"}); code != http.StatusBadRequest {
		t.Fatal("foreign file accepted")
	}
}

func TestIgnoreRulesShowWhatTheyMatched(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 1)...)
	id := patchWeb(t, f)
	route, _ := f.store.GetRoute(id)
	route.Ignore = &traffic.Rules{Paths: []string{"/health*"}, UserAgents: []string{"kube-probe"},
		Combined: []traffic.Rule{{Path: "/api/*", Methods: []string{"POST", "GET"}}}}
	if code, body := f.call(t, "POST", "/api/routes", route); code != http.StatusCreated {
		t.Fatalf("save: %d %s", code, body)
	}
	f.server.GlobalRules = traffic.Rules{UserAgents: []string{"Pingdom"}}
	hour := time.Now().Unix() / 3600
	f.server.RuleHits = func(string) []traffic.RuleHit {
		return []traffic.RuleHit{{Reason: traffic.ReasonPath, Rule: "/health*", Hours: map[int64]int64{hour: 12}, Last: time.Now()},
			{Reason: traffic.ReasonRule, Rule: "path=/api/* method=GET,POST", Hours: map[int64]int64{hour: 3}, Last: time.Now()}}
	}
	var rules IgnoreRules
	_, body := f.call(t, "GET", "/api/routes/ignore-rules?id="+url.QueryEscape(id), nil)
	json.Unmarshal(body, &rules)
	if len(rules.Route) != 3 || rules.Route[0].Kind != "rules" || rules.Route[0].Rule != "path=/api/* method=GET,POST" || rules.Route[0].Hits != 3 ||
		rules.Route[1].Rule != "/health*" || rules.Route[1].Hits != 12 || rules.Route[2].Hits != 0 {
		t.Fatalf("route rules: %s", body)
	}
	if len(rules.Global) != 1 || rules.Global[0].Rule != "Pingdom" {
		t.Fatalf("global rules: %s", body)
	}
}

func TestUptimeReportsManagedWorkloads(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 1)...)
	id := patchWeb(t, f)
	f.server.Uptime = uptime.NewRecorder()
	now := time.Now()
	f.server.Uptime.Sample(now.Add(-2*time.Minute), []uptime.State{{Namespace: "team-a", Workload: "web", Replicas: 0}, {Namespace: "team-b", Workload: "gone", Replicas: 1}})
	f.server.Uptime.Sample(now.Add(-time.Minute), []uptime.State{{Namespace: "team-a", Workload: "web", Replicas: 3}})

	var rep UptimeReport
	if code, body := f.call(t, "GET", "/api/uptime?days=7&tz=Europe/Rome", nil); code != 200 || json.Unmarshal([]byte(body), &rep) != nil {
		t.Fatalf("uptime: %d %s", code, body)
	}
	if rep.Days != 7 || rep.Timezone != "Europe/Rome" || len(rep.Workloads) != 2 || rep.Since == nil {
		t.Fatalf("report %+v", rep)
	}
	web := rep.Workloads[0]
	if web.Workload != "web" || web.Observed != 2 || web.Up != 1 || web.Replicas != 3 || web.Wakes != 1 || len(web.Routes) != 1 || web.Routes[0].ID != id || len(web.Days) != 7 || web.Hours != nil {
		t.Fatalf("web %+v", web)
	}
	if gone := rep.Workloads[1]; gone.Workload != "gone" || len(gone.Routes) != 0 {
		t.Fatalf("a workload no longer managed keeps its history, without routes: %+v", gone)
	}

	rep = UptimeReport{}
	if code, body := f.call(t, "GET", "/api/uptime?route="+url.QueryEscape(id), nil); code != 200 || json.Unmarshal([]byte(body), &rep) != nil {
		t.Fatalf("uptime of a route: %d %s", code, body)
	}
	if rep.Days != uptime.Days || len(rep.Workloads) != 1 || len(rep.Workloads[0].Hours) == 0 {
		t.Fatalf("route report %+v", rep)
	}
	if code, _ := f.call(t, "GET", "/api/uptime?route=nope", nil); code != http.StatusNotFound {
		t.Fatalf("unknown route: %d", code)
	}

	// A period of a few minutes, one workload: its hours and the minutes its replicas changed.
	period := url.Values{"from": {now.Add(-90 * time.Second).UTC().Format(time.RFC3339)}, "to": {now.UTC().Format(time.RFC3339)}, "workload": {"team-a/web"}}
	rep = UptimeReport{}
	if code, body := f.call(t, "GET", "/api/uptime?"+period.Encode(), nil); code != 200 || json.Unmarshal([]byte(body), &rep) != nil {
		t.Fatalf("uptime of a period: %d %s", code, body)
	}
	if len(rep.Workloads) != 1 || len(rep.Workloads[0].Hours) == 0 || len(rep.Workloads[0].Changes) != 1 || rep.Workloads[0].Changes[0].Replicas != 3 || rep.Workloads[0].Before == nil {
		t.Fatalf("period report %+v", rep)
	}
	for _, bad := range []string{"from=yesterday", "from=" + url.QueryEscape(now.UTC().Format(time.RFC3339)) + "&to=" + url.QueryEscape(now.Add(-time.Hour).UTC().Format(time.RFC3339))} {
		if code, _ := f.call(t, "GET", "/api/uptime?"+bad, nil); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
}
