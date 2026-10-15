package proxy

import (
	"context"
	"net/http"
	"strconv"
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
