package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"smart-proxy/internal/inspect"
	"smart-proxy/internal/logger"
)

// maxInspect is the longest a recording can be started for: it records header values, so it
// shouldn't be left on by mistake.
const maxInspect = time.Hour

// handleInspect starts (minutes > 0) or stops (minutes = 0) recording a route's requests.
func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	route, ok := s.store.GetRoute(id)
	if !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	minutes, err := strconv.Atoi(r.URL.Query().Get("minutes"))
	if err != nil || minutes < 0 || time.Duration(minutes)*time.Minute > maxInspect {
		http.Error(w, "minutes must be between 0 and 60", http.StatusBadRequest)
		return
	}
	if minutes == 0 {
		route.InspectUntil = nil
	} else {
		until := time.Now().Add(time.Duration(minutes) * time.Minute)
		route.InspectUntil = &until
	}
	if err := s.store.AddRoute(route); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if minutes == 0 {
		logger.Printf("Stopped recording requests to route %s", id)
	} else {
		logger.Printf("Recording requests to route %s for %d minute(s)", id, minutes)
	}
	writeJSON(w, map[string]any{"inspect_until": route.InspectUntil})
}

// handleRequests returns the requests recorded for a route by every replica, oldest first.
func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if _, ok := s.store.GetRoute(id); !ok {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	lists := [][]inspect.Entry{}
	if s.Inspect != nil {
		lists = append(lists, s.Inspect.Since(id, time.Time{}))
	}
	replicas := 1
	if s.Peers != nil {
		peers := s.Peers()
		replicas += len(peers)
		lists = append(lists, s.fetchPeers(r.Context(), peers, id)...)
	}
	entries := inspect.Merge(500, lists...)
	if entries == nil {
		entries = []inspect.Entry{}
	}
	writeJSON(w, map[string]any{"requests": entries, "replicas": replicas})
}

// fetchPeers asks the other replicas for their recorded requests (those not answering in time
// are skipped).
func (s *Server) fetchPeers(ctx context.Context, peers []string, routeID string) [][]inspect.Entry {
	token := ""
	if s.PeerToken != nil {
		token = s.PeerToken()
	}
	if token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var lists [][]inspect.Entry
	for _, peer := range peers {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+inspect.PeerPath+"?id="+urlQueryEscape(routeID), nil)
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var list []inspect.Entry
			if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&list) != nil {
				return
			}
			others := list[:0]
			for _, e := range list {
				if s.Inspect == nil || e.Replica != s.Inspect.Replica { // Ourselves, without POD_IP
					others = append(others, e)
				}
			}
			mu.Lock()
			lists = append(lists, others)
			mu.Unlock()
		}(peer)
	}
	wg.Wait()
	return lists
}
