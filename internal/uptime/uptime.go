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
	// Changes are the minutes its replicas changed, to the minute (since 2.7).
	Changes []Change `json:"changes,omitempty"`
	LastAt  int64    `json:"last_at,omitempty"` // Unix time of the latest sample
	LastN   int32    `json:"last_n,omitempty"`  // Replicas at the latest sample
}

// Change is a sample where a workload's replicas differ from the one before: woken, put to
// sleep, scaled. Gap marks the first sample after a time without samples (Smart Proxy not
// running): the state is unknown since the sample before (GapFrom, 0 for the first sample ever).
type Change struct {
	At       int64 `json:"t"` // Unix time
	Replicas int32 `json:"n"`
	Gap      bool  `json:"gap,omitempty"`
	GapFrom  int64 `json:"gap_from,omitempty"`
}

// maxChanges bounds the changes kept for one workload (an autoscaler can change replicas often).
const maxChanges = 5000

// gapAfter is how long without samples makes the state unknown.
const gapAfter = 150 // Seconds

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
		s.trimChanges()
	}
	return int(hour - s.Start)
}

// trimChanges drops the changes before the window, keeping the latest one before it (the state
// at the start of the window).
func (s *Series) trimChanges() {
	first := 0
	for first+1 < len(s.Changes) && s.Changes[first+1].At < s.Start*3600 {
		first++
	}
	if extra := len(s.Changes) - first - maxChanges; extra > 0 {
		first += extra
	}
	if first > 0 {
		s.Changes = append([]Change(nil), s.Changes[first:]...)
	}
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
		at := now.Unix()
		gap := !s.Sampled || s.LastAt == 0 || at-s.LastAt > gapAfter
		if gap || st.Replicas != s.LastN {
			c := Change{At: at, Replicas: st.Replicas, Gap: gap}
			if gap && s.Sampled {
				c.GapFrom = s.LastAt
			}
			s.Changes = append(s.Changes, c)
			if len(s.Changes) > maxChanges+100 {
				s.trimChanges()
			}
		}
		s.LastUp, s.Sampled, s.LastAt, s.LastN = up, true, at, st.Replicas
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
		c.Changes = append([]Change(nil), s.Changes...)
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
	Since   *time.Time `json:"since,omitempty"` // First hour recorded in the period
	Days    []Day      `json:"days"`            // Every calendar day the period touches
	Hours   []Hour     `json:"hours,omitempty"` // Every hour of the period recorded (asked for)
	Changes []Change   `json:"changes,omitempty"`
	// Before is the latest change before the period: the state at its start.
	Before *Change `json:"before,omitempty"`
}

// Options say what a report includes besides the totals and the days.
type Options struct {
	Hours, Changes bool
}

// Summarize sums a series over the hours from..to (whole hours: from rounded down to the hour):
// totals, each calendar day (in loc) and, as asked, each hour and the changes (to the second).
func Summarize(s Series, from, to time.Time, loc *time.Location, opt Options) Report {
	exact := from
	from, to = from.In(loc).Truncate(time.Hour), to.In(loc)
	rep := Report{Namespace: s.Namespace, Workload: s.Workload, Days: []Day{}}
	index := map[string]int{}
	for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		index[date] = len(rep.Days)
		rep.Days = append(rep.Days, Day{Date: date})
	}
	for i := range s.Seen {
		at := time.Unix((s.Start+int64(i))*3600, 0).In(loc)
		if at.Before(from) || !at.Before(to) {
			continue
		}
		if rep.Since == nil && s.Seen[i] > 0 {
			since := at
			rep.Since = &since
		}
		if d, ok := index[at.Format("2006-01-02")]; ok {
			rep.Days[d].add(s, i)
		}
		rep.Totals.add(s, i)
		if opt.Hours {
			h := Hour{At: at}
			h.add(s, i)
			rep.Hours = append(rep.Hours, h)
		}
	}
	if opt.Changes {
		for i, c := range s.Changes {
			if c.At < exact.Unix() {
				rep.Before = &s.Changes[i]
				continue
			}
			if c.At >= to.Unix() {
				break
			}
			rep.Changes = append(rep.Changes, c)
		}
	}
	return rep
}

// LastDays is the period of the last n calendar days in loc, today included.
func LastDays(now time.Time, n int, loc *time.Location) (time.Time, time.Time) {
	now = now.In(loc)
	return time.Date(now.Year(), now.Month(), now.Day()-(n-1), 0, 0, 0, 0, loc), now
}
