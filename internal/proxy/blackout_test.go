package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"smart-proxy/internal/blackout"
	"smart-proxy/internal/store"
)

func TestBlockedHoursDontWakeTheApp(t *testing.T) {
	h, c, id := monitorHandler(t, "")
	now := time.Now().UTC()
	s := blackout.Defaults()
	s.Timezone = "UTC"
	s.Global = store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{
		{From: now.Add(-time.Hour).Format("15:04"), To: now.Add(2 * time.Hour).Format("15:04")},
	}}
	h.Blackout = &blackout.Keeper{}
	if err := h.Blackout.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	w := get(h, "/", "Mozilla/5.0")
	retry, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Smart-Proxy") != "blocked" || retry < 3600 || retry > 2*3600+60 {
		t.Fatalf("blocked: %d %v", w.Code, w.Header())
	}
	if replicas(t, c, "web") != 0 {
		t.Fatal("the app was woken during its blocked hours")
	}

	// An exception lets requests wake it.
	r, _ := h.store.GetRoute(id)
	r.Exceptions = []store.Exception{{ID: "x", Kind: store.ExceptionAllow, From: now.Add(-time.Minute), To: now.Add(time.Hour)}}
	h.store.AddRoute(r)
	if w := get(h, "/", "Mozilla/5.0"); w.Header().Get("X-Smart-Proxy") == "blocked" || replicas(t, c, "web") == 0 {
		t.Fatalf("with an exception: %d %v", w.Code, w.Header())
	}
}

// With "ask", browsers get a page with a button starting the app; other clients still get 503.
func TestBlockedHoursPageStartsTheAppOnRequest(t *testing.T) {
	h, c, _ := monitorHandler(t, "")
	now := time.Now().UTC()
	s := blackout.Defaults()
	s.Timezone = "UTC"
	s.Global = store.BlackoutPolicy{Mode: store.BlackoutCustom, WhenBlocked: store.WhenBlockedAsk, Message: "Urgent? Call the on-call team.", Windows: []store.BlackoutWindow{
		{From: now.Add(-time.Hour).Format("15:04"), To: now.Add(2 * time.Hour).Format("15:04")},
	}}
	h.Blackout = &blackout.Keeper{}
	if err := h.Blackout.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	page := get(h, "/orders?id=7", "Mozilla/5.0") // Accept: text/html
	body := page.Body.String()
	if page.Code != http.StatusOK || page.Header().Get("X-Smart-Proxy") != "blocked" || !strings.Contains(body, `action="/orders/__smart_proxy/start"`) ||
		!strings.Contains(body, "Urgent? Call the on-call team.") || !strings.Contains(body, `value="id=7"`) {
		t.Fatalf("page: %d %s", page.Code, body)
	}
	api := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://web.example.com/api/orders", nil)
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(api, req)
	if api.Code != http.StatusServiceUnavailable || strings.Contains(api.Body.String(), "<form") {
		t.Fatalf("API call: %d %s", api.Code, api.Body.String())
	}

	start := func(method string, header http.Header) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://web.example.com/orders/__smart_proxy/start", strings.NewReader("path=%2Forders&query=id%3D7"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range header {
			r.Header[k] = v
		}
		h.ServeHTTP(w, r)
		return w
	}
	if w := start("GET", nil); w.Code != http.StatusMethodNotAllowed || replicas(t, c, "web") != 0 {
		t.Fatalf("GET started it: %d", w.Code)
	}
	if w := start("POST", http.Header{"Origin": {"https://elsewhere.example"}}); w.Code != http.StatusForbidden || replicas(t, c, "web") != 0 {
		t.Fatalf("cross-site POST: %d", w.Code)
	}
	w := start("POST", http.Header{"Origin": {"https://web.example.com:8443"}, "Sec-Fetch-Site": {"same-origin"}}) // A port too
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/orders?id=7" || replicas(t, c, "web") == 0 {
		t.Fatalf("start: %d %v replicas %d", w.Code, w.Header(), replicas(t, c, "web"))
	}
	// Starting: the page is no longer offered, the waking page is.
	if again := get(h, "/orders?id=7", "Mozilla/5.0"); again.Header().Get("X-Smart-Proxy") == "blocked" {
		t.Fatalf("after starting: %d %v", again.Code, again.Header())
	}
}
