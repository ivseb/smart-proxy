package admin

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/blackout"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// handleSettings reads (GET) or replaces (PUT) the installation-wide settings: time zone,
// holidays, and the blocked hours of routes that don't set their own (global, per namespace).
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.blackoutSettings())
	case http.MethodPut:
		if s.Blackout == nil {
			http.Error(w, "Settings unavailable", http.StatusServiceUnavailable)
			return
		}
		settings := blackout.Defaults()
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&settings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for ns, p := range settings.Namespaces {
			if p.Mode == "" { // Follows the global setting: nothing to keep
				delete(settings.Namespaces, ns)
			}
		}
		if err := settings.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Blackout.Save(r.Context(), settings); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logger.Printf("Settings changed by %s: blocked hours %s, %d namespace(s) with their own, %d holiday(s), time zone %s",
			orSomeone(auth.UserFromContext(r.Context())), describePolicy(settings.Global), len(settings.Namespaces), len(settings.Holidays), settings.Timezone)
		writeJSON(w, s.blackoutSettings())
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func describePolicy(p store.BlackoutPolicy) string {
	if p.Mode != store.BlackoutCustom {
		return "off"
	}
	return blackout.Describe(p.Windows)
}

// BlockedHoursView is a route's blocked hours: their state now and the week ahead.
type BlockedHoursView struct {
	State    blackout.State `json:"state"`
	Week     []blackout.Day `json:"week"`
	Timezone string         `json:"timezone"`
}

// handleBlockedHours shows a route's blocked hours (GET id=...): their state and the 7 days ahead.
func (s *Server) handleBlockedHours(w http.ResponseWriter, r *http.Request) {
	route, ok := s.store.GetRoute(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	settings, now := s.blackoutSettings(), time.Now()
	writeJSON(w, BlockedHoursView{State: settings.State(*route, now), Week: settings.Week(*route, now), Timezone: settings.Timezone})
}

const (
	maxExceptions    = 20                  // Per route, not yet ended
	maxExceptionSpan = 31 * 24 * time.Hour // An exception is for a while, not forever
	keepEnded        = 7 * 24 * time.Hour  // Ended exceptions stay visible this long
)

// handleExceptions adds an exception to a route's blocked hours (POST id=..., body: kind
// "allow" or "on", from, to, reason) or ends one (DELETE id=...&exception=...: ended now if in
// force, removed otherwise).
func (s *Server) handleExceptions(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	route, ok := s.store.GetRoute(id)
	if !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	user := auth.UserFromContext(r.Context())
	now := time.Now()
	switch r.Method {
	case http.MethodPost:
		var e store.Exception
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case e.Kind != store.ExceptionAllow && e.Kind != store.ExceptionOn:
			http.Error(w, `kind must be "allow" or "on"`, http.StatusBadRequest)
			return
		case !e.To.After(e.From) || !e.To.After(now):
			http.Error(w, "the exception must end after it starts, in the future", http.StatusBadRequest)
			return
		case e.To.Sub(e.From) > maxExceptionSpan:
			http.Error(w, "an exception lasts 31 days at most", http.StatusBadRequest)
			return
		case len(e.Reason) > 200:
			http.Error(w, "the reason is too long (200 characters at most)", http.StatusBadRequest)
			return
		}
		if e.From.Before(now) {
			e.From = now
		}
		e.ID, e.By, e.At = uuid.NewString()[:8], user, now
		route.Exceptions = slices.DeleteFunc(route.Exceptions, func(x store.Exception) bool { return now.Sub(x.To) > keepEnded })
		pending := 0
		for _, x := range route.Exceptions {
			if x.To.After(now) {
				pending++
			}
		}
		if pending >= maxExceptions {
			http.Error(w, "too many exceptions for this route: remove some first", http.StatusBadRequest)
			return
		}
		route.Exceptions = append(route.Exceptions, e)
		slices.SortFunc(route.Exceptions, func(a, b store.Exception) int { return a.From.Compare(b.From) })
		if err := s.store.AddRoute(route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logger.Printf("Route %s: exception to its blocked hours (%s) from %s to %s, by %s", id, e.Kind, e.From.Format(time.RFC3339), e.To.Format(time.RFC3339), orSomeone(user))
		if e.Kind == store.ExceptionOn && e.Active(now) {
			s.wakeKeptOn(*route) // Kept on from now: up now
		}
	case http.MethodDelete:
		exception := r.URL.Query().Get("exception")
		i := slices.IndexFunc(route.Exceptions, func(x store.Exception) bool { return x.ID == exception })
		if i < 0 {
			http.Error(w, "Exception not found", http.StatusNotFound)
			return
		}
		if route.Exceptions[i].Active(now) {
			// Ended rather than removed: if the route is blocked again, its application is
			// put to sleep, as when an exception ends by itself.
			route.Exceptions[i].To = now
		} else {
			route.Exceptions = slices.Delete(route.Exceptions, i, i+1)
		}
		if err := s.store.AddRoute(route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logger.Printf("Route %s: exception %s ended by %s", id, exception, orSomeone(user))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"exceptions": route.Exceptions})
}
