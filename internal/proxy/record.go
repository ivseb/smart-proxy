package proxy

import (
	"net"
	"net/http"
	"time"

	"smart-proxy/internal/inspect"
	"smart-proxy/internal/store"
)

// recording is a request being recorded for the dashboard's inspector. Its methods do nothing
// on a nil recording (the route isn't inspected).
type recording struct {
	h       *Handler
	routeID string
	start   time.Time
	entry   inspect.Entry
	writer  *inspect.StatusWriter
	upgrade bool
}

func (h *Handler) record(w http.ResponseWriter, r *http.Request, client net.IP, route store.RouteConfig) *recording {
	return &recording{
		h: h, routeID: route.ID, start: time.Now(),
		entry:   inspect.Describe(r, client),
		writer:  &inspect.StatusWriter{ResponseWriter: w},
		upgrade: r.Header.Get("Upgrade") != "",
	}
}

func (rec *recording) set(outcome string) {
	if rec != nil {
		rec.entry.Outcome = outcome
	}
}

func (rec *recording) target(t target) {
	if rec != nil {
		rec.entry.Backend, rec.entry.Why = t.Service, t.Why
		if rec.entry.Outcome == "" {
			rec.entry.Outcome = inspect.OutcomeProxied
		}
	}
}

func (rec *recording) setUser(user string) {
	if rec != nil {
		rec.entry.User = user
	}
}

// finish stores the request once answered.
func (rec *recording) finish() {
	status := rec.writer.Status
	if status == 0 && rec.upgrade {
		status = http.StatusSwitchingProtocols // Hijacked by the upgrade
	}
	rec.entry.Status = status
	rec.entry.DurationMs = float64(time.Since(rec.start).Microseconds()) / 1000
	rec.h.Inspect.Add(rec.routeID, rec.entry)
}
