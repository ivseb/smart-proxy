// Package history keeps recent request rates in memory, so the dashboard's charts survive a page
// reload. Each replica samples the request counters of every replica at a fixed interval and
// keeps the last samples; nothing is stored.
package history

import (
	"context"
	"sync"
	"time"
)

// Counts are request counters, overall and per route, as published by one replica.
type Counts struct {
	Total  int64
	Routes map[string]int64
}

// Point holds the requests received during one interval, ending at At.
type Point struct {
	At       time.Time        `json:"at"`
	Requests int64            `json:"requests"`
	Routes   map[string]int64 `json:"routes,omitempty"`
}

// Recorder samples counters and keeps the requests of each interval for the retention period.
type Recorder struct {
	// Sample returns the counters of every replica, keyed by replica. Counters only grow, but
	// restart from zero when a replica restarts.
	Sample    func() map[string]Counts
	Interval  time.Duration
	Retention time.Duration

	mu     sync.Mutex
	points []Point
	last   map[string]Counts // nil until the first sample
}

// Run samples until the context is done.
func (r *Recorder) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	r.Record(time.Now(), r.Sample())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Record(now, r.Sample())
		}
	}
}

// Record adds the requests received since the previous sample.
func (r *Recorder) Record(now time.Time, sample map[string]Counts) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		r.last = sample // the baseline: requests before it are not part of any interval
		return
	}
	p := Point{At: now, Routes: map[string]int64{}}
	for source, cur := range sample {
		prev, known := r.last[source]
		p.Requests += delta(prev.Total, cur.Total, known)
		for id, n := range cur.Routes {
			if d := delta(prev.Routes[id], n, known); d > 0 {
				p.Routes[id] += d
			}
		}
	}
	r.last = sample
	r.points = append(r.points, p)
	cutoff := now.Add(-r.Retention)
	i := 0
	for i < len(r.points) && !r.points[i].At.After(cutoff) {
		i++
	}
	r.points = r.points[i:]
}

// delta is what a counter grew by. A counter that went backwards was restarted; one not seen
// before belongs to a replica that just started publishing.
func delta(prev, cur int64, known bool) int64 {
	if !known || cur < prev {
		return cur
	}
	return cur - prev
}

// Points returns the recorded intervals, oldest first.
func (r *Recorder) Points() []Point {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Point(nil), r.points...)
}
