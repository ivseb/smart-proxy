package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"smart-proxy/internal/blackout"
	"smart-proxy/internal/store"
)

func TestSettingsAreValidatedAndSaved(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 1)...)
	f.server.Blackout = &blackout.Keeper{}

	var got blackout.Settings
	if code, body := f.call(t, "GET", "/api/settings", nil); code != 200 || json.Unmarshal(body, &got) != nil || got.Timezone != "Europe/Rome" || got.Global.Mode != store.BlackoutOff {
		t.Fatalf("defaults: %d %s", code, body)
	}
	bad := `{"timezone":"Europe/Rome","global":{"mode":"custom","windows":[{"from":"22:00","to":"22:00"}]}}`
	if code, _ := f.call(t, "PUT", "/api/settings", json.RawMessage(bad)); code != http.StatusBadRequest {
		t.Fatalf("invalid window accepted: %d", code)
	}
	good := `{"timezone":"Europe/Rome","holidays":[{"date":"12-25","name":"Natale"}],
		"global":{"mode":"custom","windows":[{"days":["mon","tue","wed","thu","fri"],"from":"22:00","to":"06:00"},{"days":["sat","sun"],"from":"00:00","to":"24:00"}]},
		"namespaces":{"team-a":{"mode":"off"},"team-b":{"mode":""}}}`
	if code, body := f.call(t, "PUT", "/api/settings", json.RawMessage(good)); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	s := f.server.Blackout.Get()
	if len(s.Global.Windows) != 2 || s.Namespaces["team-a"].Mode != store.BlackoutOff || len(s.Namespaces) != 1 || len(s.Holidays) != 1 {
		t.Fatalf("saved: %+v", s)
	}
}

func TestExceptions(t *testing.T) {
	f := newFixture(t, app("team-a", "web", 0)...)
	f.server.Blackout = &blackout.Keeper{}
	id := patchWeb(t, f)
	path := "/api/routes/exceptions?id=" + url.QueryEscape(id)
	now := time.Now()
	exception := func(kind string, from, to time.Time) string {
		b, _ := json.Marshal(map[string]any{"kind": kind, "from": from, "to": to, "reason": "release on Saturday"})
		return string(b)
	}

	for name, body := range map[string]string{
		"kind":     exception("maybe", now, now.Add(time.Hour)),
		"past":     exception("allow", now.Add(-2*time.Hour), now.Add(-time.Hour)),
		"reversed": exception("allow", now.Add(2*time.Hour), now.Add(time.Hour)),
		"too long": exception("allow", now, now.Add(40*24*time.Hour)),
	} {
		if code, _ := f.call(t, "POST", path, json.RawMessage(body)); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}

	// Kept on from now: woken right away.
	if code, body := f.call(t, "POST", path, json.RawMessage(exception("on", now.Add(-time.Minute), now.Add(time.Hour)))); code != 200 {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, body := f.call(t, "POST", path, json.RawMessage(exception("allow", now.Add(24*time.Hour), now.Add(30*time.Hour)))); code != 200 {
		t.Fatalf("add a future one: %d %s", code, body)
	}
	route, _ := f.store.GetRoute(id)
	if len(route.Exceptions) != 2 || route.Exceptions[0].Kind != store.ExceptionOn || !route.KeptOn(time.Now()) {
		t.Fatalf("exceptions: %+v", route.Exceptions)
	}
	if d, _ := f.cluster.Kube.AppsV1().Deployments("team-a").Get(context.TODO(), "web", metav1.GetOptions{}); *d.Spec.Replicas != 1 {
		t.Fatalf("kept on from now, not woken: %d", *d.Spec.Replicas)
	}

	// Saving the route from a form doesn't drop them.
	route.Exceptions = nil
	b, _ := json.Marshal(route)
	if code, body := f.call(t, "POST", "/api/routes", json.RawMessage(b)); code != http.StatusCreated {
		t.Fatalf("save: %d %s", code, body)
	}
	route, _ = f.store.GetRoute(id)
	if len(route.Exceptions) != 2 {
		t.Fatalf("exceptions lost on save: %+v", route.Exceptions)
	}

	// Ending: the one in force ends now (kept, ended), the future one is removed.
	active, future := route.Exceptions[0].ID, route.Exceptions[1].ID
	for _, e := range []string{active, future} {
		if code, body := f.call(t, "DELETE", path+"&exception="+e, nil); code != 200 {
			t.Fatalf("end %s: %d %s", e, code, body)
		}
	}
	route, _ = f.store.GetRoute(id)
	if len(route.Exceptions) != 1 || route.Exceptions[0].ID != active || route.Exceptions[0].Active(time.Now()) {
		t.Fatalf("after ending: %+v", route.Exceptions)
	}

	// The route's state and week.
	var view BlockedHoursView
	if code, body := f.call(t, "GET", "/api/routes/blocked-hours?id="+url.QueryEscape(id), nil); code != 200 || json.Unmarshal(body, &view) != nil || len(view.Week) != 7 || view.State.Source != blackout.SourceGlobal {
		t.Fatalf("blocked hours: %d %s", code, body)
	}
}
