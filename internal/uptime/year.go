package uptime

import (
	"sort"
	"time"
)

// YearDays is how long the year's history goes back.
const YearDays = 366

// YearSeries is a workload's history day by day over the last year (days in YearTimezone):
// index i is the day Start+i (days since 1970-01-01).
type YearSeries struct {
	Namespace string   `json:"ns"`
	Workload  string   `json:"w"`
	Start     int64    `json:"start"`
	Observed  []uint16 `json:"obs"`  // Minutes observed
	Up        []uint16 `json:"up"`   // Minutes with at least one replica
	Replicas  []uint32 `json:"rep"`  // Replica minutes
	Baseline  []uint32 `json:"base"` // Replica minutes always on (see Series.Baseline)
	Wakes     []uint16 `json:"wakes"`
}

// dayNumber is a day's number (days since 1970-01-01) of a moment, in loc.
func dayNumber(t time.Time, loc *time.Location) int64 {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// dayDate is a day number's date.
func dayDate(day int64) time.Time { return time.Unix(day*86400, 0).UTC() }

func (y *YearSeries) at(day int64) int {
	if len(y.Observed) == 0 {
		y.Start = day
	}
	if day < y.Start {
		return -1
	}
	for int64(len(y.Observed)) <= day-y.Start {
		y.Observed = append(y.Observed, 0)
		y.Up = append(y.Up, 0)
		y.Replicas = append(y.Replicas, 0)
		y.Baseline = append(y.Baseline, 0)
		y.Wakes = append(y.Wakes, 0)
	}
	if drop := len(y.Observed) - YearDays; drop > 0 {
		y.Observed, y.Up, y.Replicas, y.Baseline, y.Wakes = y.Observed[drop:], y.Up[drop:], y.Replicas[drop:], y.Baseline[drop:], y.Wakes[drop:]
		y.Start += int64(drop)
	}
	return int(day - y.Start)
}

// sampleYear adds one minute of a workload to its day (r.mu held).
func (r *Recorder) sampleYear(now time.Time, st State, base uint32, wake bool) {
	k := key(st.Namespace, st.Workload)
	y := r.year[k]
	if y == nil {
		y = &YearSeries{Namespace: st.Namespace, Workload: st.Workload}
		r.year[k] = y
	}
	i := y.at(dayNumber(now, r.loc))
	if i < 0 || y.Observed[i] >= 1440 {
		return
	}
	y.Observed[i]++
	y.Baseline[i] += base
	if st.Replicas > 0 {
		y.Up[i]++
		y.Replicas[i] += uint32(st.Replicas)
	}
	if wake {
		y.Wakes[i]++
	}
}

// SnapshotYear returns a copy of the year's history, dropping workloads with nothing in it.
func (r *Recorder) SnapshotYear(now time.Time) []YearSeries {
	from := dayNumber(now, r.loc) - YearDays + 1
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]YearSeries, 0, len(r.year))
	for k, y := range r.year {
		if y.Start+int64(len(y.Observed)) <= from {
			delete(r.year, k)
			continue
		}
		c := *y
		c.Observed = append([]uint16(nil), y.Observed...)
		c.Up = append([]uint16(nil), y.Up...)
		c.Replicas = append([]uint32(nil), y.Replicas...)
		c.Baseline = append([]uint32(nil), y.Baseline...)
		c.Wakes = append([]uint16(nil), y.Wakes...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return key(out[i].Namespace, out[i].Workload) < key(out[j].Namespace, out[j].Workload)
	})
	return out
}

// ReplaceYear sets the year's history (loaded from its ConfigMap). With none yet, it is built
// from the hourly history (the days recorded before 2.8).
func (r *Recorder) ReplaceYear(year []YearSeries) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.year = make(map[string]*YearSeries, len(year))
	for i := range year {
		y := year[i]
		r.year[key(y.Namespace, y.Workload)] = &y
	}
	if len(year) > 0 {
		return
	}
	for k, s := range r.series {
		s.fillBaseline()
		y := &YearSeries{Namespace: s.Namespace, Workload: s.Workload}
		for i := range s.Seen {
			d := y.at(dayNumber(time.Unix((s.Start+int64(i))*3600, 0), r.loc))
			if d < 0 {
				continue
			}
			y.Observed[d] += uint16(s.Seen[i])
			y.Up[d] += uint16(s.Up[i])
			y.Replicas[d] += s.Replicas[i]
			y.Baseline[d] += s.Baseline[i]
			y.Wakes[d] += s.Wakes[i]
		}
		if len(y.Observed) > 0 {
			r.year[k] = y
		}
	}
}

// Month is one calendar month of the year's history.
type Month struct {
	Month string `json:"month"` // YYYY-MM
	Totals
}

// YearReport is a workload's last 12 months, month by month (the current one so far).
type YearReport struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Totals
	Since  *time.Time `json:"since,omitempty"` // First day recorded
	Months []Month    `json:"months"`
}

// LastMonths are the 12 months ending with now's (in the year's time zone): "YYYY-MM".
func LastMonths(now time.Time) []string {
	loc, err := time.LoadLocation(YearTimezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	out := make([]string, 12)
	for i := range out {
		out[i] = time.Date(local.Year(), local.Month()-time.Month(11-i), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	}
	return out
}

// SummarizeYear sums a workload's year month by month, over the last 12 months.
func SummarizeYear(y YearSeries, now time.Time) YearReport {
	months := LastMonths(now)
	rep := YearReport{Namespace: y.Namespace, Workload: y.Workload, Months: make([]Month, len(months))}
	index := make(map[string]int, len(months))
	for i, m := range months {
		rep.Months[i].Month = m
		index[m] = i
	}
	for i := range y.Observed {
		date := dayDate(y.Start + int64(i))
		m, ok := index[date.Format("2006-01")]
		if !ok || y.Observed[i] == 0 {
			continue
		}
		if rep.Since == nil {
			rep.Since = &date
		}
		t := Totals{Observed: int64(y.Observed[i]), Up: int64(y.Up[i]), Replicas: int64(y.Replicas[i]), Baseline: int64(y.Baseline[i]), Wakes: int64(y.Wakes[i])}
		rep.Months[m].plus(t)
		rep.Totals.plus(t)
	}
	return rep
}

func (t *Totals) plus(o Totals) {
	t.Observed += o.Observed
	t.Up += o.Up
	t.Replicas += o.Replicas
	t.Baseline += o.Baseline
	t.Wakes += o.Wakes
}
