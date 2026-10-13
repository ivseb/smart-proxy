// Package uptime keeps, for every workload Smart Proxy manages, how long it was up (at least one
// replica) and down over the last 30 days, hour by hour: minutes up, minutes observed, replica
// minutes and wake-ups. The leader samples the workloads every minute; the history is kept in a
// ConfigMap (gzipped), so it survives restarts and leader changes, and the other replicas read it.
package uptime

import (
	"sort"
	"sync"
	"time"
)

// Days is how long the history goes back.
const Days = 30

const window = Days * 24 // Hours kept

// Series is the history of one workload: index i is the UTC hour Start+i.
type Series struct {
	Namespace string   `json:"ns"`
	Workload  string   `json:"w"`
	Start     int64    `json:"start"`   // Unix hour of the first bucket
	Seen      []byte   `json:"seen"`    // Minutes observed in each hour
	Up        []byte   `json:"up"`      // Minutes with at least one replica
	Replicas  []uint32 `json:"rep"`     // Replica minutes (a minute with 3 replicas counts 3)
	Wakes     []uint16 `json:"wakes"`   // Times it went from no replica to some
	LastUp    bool     `json:"last_up"` // State at the latest sample, to see wake-ups
	Sampled   bool     `json:"sampled"` // Sampled at least once
}

func key(namespace, workload string) string { return namespace + "/" + workload }

// at returns the bucket index of an hour, growing the series (and dropping hours beyond the
// window); -1 for an hour before the series.
func (s *Series) at(hour int64) int {
	if len(s.Seen) == 0 {
		s.Start = hour
	}
	if hour < s.Start {
		return -1
	}
	for int64(len(s.Seen)) <= hour-s.Start {
		s.Seen = append(s.Seen, 0)
		s.Up = append(s.Up, 0)
		s.Replicas = append(s.Replicas, 0)
		s.Wakes = append(s.Wakes, 0)
	}
	if drop := len(s.Seen) - window; drop > 0 {
		s.Seen, s.Up, s.Replicas, s.Wakes = s.Seen[drop:], s.Up[drop:], s.Replicas[drop:], s.Wakes[drop:]
		s.Start += int64(drop)
	}
	return int(hour - s.Start)
}

// State is a workload's replicas at a sample.
type State struct {
	Namespace, Workload string
	Replicas            int32
}

// Recorder holds the history of every workload.
type Recorder struct {
	mu     sync.Mutex
	series map[string]*Series
	dirty  bool
}

func NewRecorder() *Recorder { return &Recorder{series: map[string]*Series{}} }

// Sample records one minute of the given workloads.
func (r *Recorder) Sample(now time.Time, states []State) {
	hour := now.Unix() / 3600
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range states {
		k := key(st.Namespace, st.Workload)
		s := r.series[k]
		if s == nil {
			s = &Series{Namespace: st.Namespace, Workload: st.Workload}
			r.series[k] = s
		}
		i := s.at(hour)
		if i < 0 {
			continue
		}
		up := st.Replicas > 0
		if s.Seen[i] < 60 {
			s.Seen[i]++
			if up {
				s.Up[i]++
			}
		}
		if up {
			s.Replicas[i] += uint32(st.Replicas)
			if s.Sampled && !s.LastUp {
				s.Wakes[i]++
			}
		}
		s.LastUp, s.Sampled = up, true
	}
	r.dirty = true
}

// Snapshot returns a copy of every series, dropping those with nothing in the window.
func (r *Recorder) Snapshot(now time.Time) []Series {
	from := now.Unix()/3600 - window + 1
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Series, 0, len(r.series))
	for k, s := range r.series {
		if s.Start+int64(len(s.Seen)) <= from {
			delete(r.series, k)
			continue
		}
		c := *s
		c.Seen = append([]byte(nil), s.Seen...)
		c.Up = append([]byte(nil), s.Up...)
		c.Replicas = append([]uint32(nil), s.Replicas...)
		c.Wakes = append([]uint16(nil), s.Wakes...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return key(out[i].Namespace, out[i].Workload) < key(out[j].Namespace, out[j].Workload)
	})
	return out
}

// Replace sets the history (loaded from the ConfigMap).
func (r *Recorder) Replace(series []Series) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.series = make(map[string]*Series, len(series))
	for i := range series {
		s := series[i]
		r.series[key(s.Namespace, s.Workload)] = &s
	}
	r.dirty = false
}

// TakeDirty reports whether samples were added since the last call.
func (r *Recorder) TakeDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.dirty
	r.dirty = false
	return d
}

// Totals sum a period of a series.
type Totals struct {
	Observed int64 `json:"observed_minutes"`
	Up       int64 `json:"up_minutes"`
	Replicas int64 `json:"replica_minutes"`
	Wakes    int64 `json:"wakes"`
}

func (t *Totals) add(s Series, i int) {
	t.Observed += int64(s.Seen[i])
	t.Up += int64(s.Up[i])
	t.Replicas += int64(s.Replicas[i])
	t.Wakes += int64(s.Wakes[i])
}

// Day is one calendar day of a series, in a time zone.
type Day struct {
	Date string `json:"date"` // YYYY-MM-DD
	Totals
}

// Hour is one hour of a series.
type Hour struct {
	At time.Time `json:"at"`
	Totals
}

// Report is a workload's history over a period.
type Report struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Totals
	Since *time.Time `json:"since,omitempty"` // First hour recorded in the period
	Days  []Day      `json:"days"`
	Hours []Hour     `json:"hours,omitempty"`
}

// Summarize sums a series over the days (in loc) ending today: totals, each day and, with
// hourly, each hour.
func Summarize(s Series, now time.Time, days int, loc *time.Location, hourly bool) Report {
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	first := today.AddDate(0, 0, -(days - 1))
	rep := Report{Namespace: s.Namespace, Workload: s.Workload, Days: make([]Day, days)}
	for d := range rep.Days {
		rep.Days[d].Date = first.AddDate(0, 0, d).Format("2006-01-02")
	}
	index := map[string]int{}
	for d, day := range rep.Days {
		index[day.Date] = d
	}
	for i := range s.Seen {
		at := time.Unix((s.Start+int64(i))*3600, 0).In(loc)
		if at.Before(first) || at.After(now) {
			continue
		}
		d, ok := index[at.Format("2006-01-02")]
		if !ok {
			continue
		}
		if rep.Since == nil && s.Seen[i] > 0 {
			since := at
			rep.Since = &since
		}
		rep.Days[d].add(s, i)
		rep.Totals.add(s, i)
		if hourly {
			h := Hour{At: at}
			h.add(s, i)
			rep.Hours = append(rep.Hours, h)
		}
	}
	return rep
}
