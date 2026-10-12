package admin

import (
	"net/http"
	"time"

	"smart-proxy/internal/auth"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// handlePause pauses a route (POST, until=<RFC 3339 time>, or no until: until resumed) or
// resumes it (DELETE). While paused Smart Proxy only forwards its requests to the application:
// it is woken now and doesn't sleep until the pause ends.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	route, ok := s.store.GetRoute(id)
	if !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodPost:
		pause := &store.Pause{At: time.Now(), By: auth.UserFromContext(r.Context())}
		if v := r.URL.Query().Get("until"); v != "" {
			until, err := time.Parse(time.RFC3339, v)
			if err != nil || !until.After(time.Now()) {
				http.Error(w, "until must be a time in the future (RFC 3339)", http.StatusBadRequest)
				return
			}
			pause.Until = &until
		}
		route.Pause = pause
		if err := s.store.AddRoute(route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.wakeForPause(*route)
		if pause.Until != nil {
			logger.Printf("Route %s paused until %s by %s", id, pause.Until.Format(time.RFC3339), orSomeone(pause.By))
		} else {
			logger.Printf("Route %s paused until resumed, by %s", id, orSomeone(pause.By))
		}
	case http.MethodDelete:
		if route.Pause == nil {
			break
		}
		route.Pause = nil
		if err := s.store.AddRoute(route); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.store.UpdateActivity(id) // Its idle timeout starts now
		logger.Printf("Route %s resumed by %s", id, orSomeone(auth.UserFromContext(r.Context())))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"pause": route.Pause})
}

// wakeForPause wakes a paused route's application and dependencies: a plain reverse proxy needs
// them up.
func (s *Server) wakeForPause(route store.RouteConfig) {
	if s.k8sClient == nil {
		return
	}
	for _, name := range append([]string{route.Deployment}, dependencyNames(route.Dependencies)...) {
		if target, err := s.k8sClient.WakeDeployment(route.Namespace, name); err != nil {
			logger.Printf("Warning: waking %s/%s for its paused route: %v", route.Namespace, name, err)
		} else if target > 0 {
			logger.Printf("Woke %s/%s with %d replica(s): its route is paused", route.Namespace, name, target)
		}
	}
}

func orSomeone(user string) string {
	if user == "" {
		return "the dashboard"
	}
	return user
}
