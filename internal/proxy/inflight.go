package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"smart-proxy/internal/logger"
	"smart-proxy/internal/store"
)

// activeEvery is how often a route with requests in progress (WebSockets, streams, long
// downloads) is marked active, so it isn't put to sleep while in use.
const activeEvery = 10 * time.Second

// inflight counts the requests in progress for each route.
type inflight struct {
	once   sync.Once
	mu     sync.Mutex
	counts map[string]int
}

// track marks a route active while a request is in progress; call the returned func when it ends.
func (h *Handler) track(routeID string) func() {
	f := &h.inflight
	f.once.Do(func() {
		f.counts = map[string]int{}
		go h.markInflightActive()
	})
	f.mu.Lock()
	f.counts[routeID]++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		if f.counts[routeID]--; f.counts[routeID] <= 0 {
			delete(f.counts, routeID)
		}
		f.mu.Unlock()
		h.store.UpdateActivity(routeID)
	}
}

func (h *Handler) markInflightActive() {
	for range time.Tick(activeEvery) {
		h.inflight.mu.Lock()
		ids := make([]string, 0, len(h.inflight.counts))
		for id := range h.inflight.counts {
			ids = append(ids, id)
		}
		h.inflight.mu.Unlock()
		for _, id := range ids {
			h.store.UpdateActivity(id)
		}
	}
}

// waitAwake wakes a route and waits until it can serve, the request is cancelled or the wake
// timeout expires.
func (h *Handler) waitAwake(ctx context.Context, route store.RouteConfig) bool {
	timeout := h.WakeTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, ready := h.ensureAwake([]store.RouteConfig{route}); ready || h.hasServingPassThrough(route) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// waitReachable waits until a Service accepts connections: right after pods become ready, the
// Service's endpoints may not have reached every node yet.
func waitReachable(ctx context.Context, addr string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	for {
		dialCtx, cancelDial := context.WithTimeout(ctx, time.Second)
		conn, err := dialer.DialContext(dialCtx, "tcp", addr)
		cancelDial()
		if err == nil {
			conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// transport is shared by all proxied requests, keeping connections to the applications alive.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 512
	t.MaxIdleConnsPerHost = 64
	return t
}()

// proxyError answers a request the application couldn't take. Clients that went away are not
// worth a log line.
func proxyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		return
	}
	logger.Printf("Proxy error for %s%s: %v", r.Host, r.URL.Path, err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintln(w, "Smart Proxy: the application is not reachable.")
}
