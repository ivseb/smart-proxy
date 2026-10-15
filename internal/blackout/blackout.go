// Package blackout decides when sleeping applications must not be woken: blocked hours (e.g.
// weeknights and weekends), set globally, per namespace or per route, with holidays counting as
// Sundays, and exceptions lifting them for a while. A blocked application isn't woken by
// requests (they get 503), and is put to sleep when its blocked hours start.
package blackout

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"smart-proxy/internal/store"
)

// DefaultTimezone is the time zone of the blocked hours and holidays when none is set.
const DefaultTimezone = "Europe/Rome"

// Holiday is a date that counts as a Sunday: "12-25" every year, or "2027-03-29" once.
type Holiday struct {
	Date string `json:"date"`
	Name string `json:"name,omitempty"`
}

var holidayDate = regexp.MustCompile(`^(\d{4}-)?\d{2}-\d{2}$`)

// Settings are the installation-wide settings: the time zone, the holidays and the blocked
// hours of the routes that don't set their own (by namespace, then global).
type Settings struct {
	Timezone   string                          `json:"timezone"`
	Holidays   []Holiday                       `json:"holidays"`
	Global     store.BlackoutPolicy            `json:"global"`
	Namespaces map[string]store.BlackoutPolicy `json:"namespaces"`
}

// Defaults are the settings before anyone changes them: no blocked hours.
func Defaults() Settings {
	return Settings{Timezone: DefaultTimezone, Holidays: []Holiday{}, Global: store.BlackoutPolicy{Mode: store.BlackoutOff}, Namespaces: map[string]store.BlackoutPolicy{}}
}

// Validate reports invalid settings.
func (s Settings) Validate() error {
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" {
		return fmt.Errorf("invalid time zone %q", s.Timezone)
	}
	for _, h := range s.Holidays {
		if !holidayDate.MatchString(h.Date) {
			return fmt.Errorf("invalid holiday %q (MM-DD every year, or YYYY-MM-DD)", h.Date)
		}
		layout, value := "2006-01-02", h.Date
		if len(h.Date) == 5 {
			value = "2000-" + h.Date // A leap year: 02-29 is a date
		}
		if _, err := time.Parse(layout, value); err != nil {
			return fmt.Errorf("invalid holiday %q", h.Date)
		}
	}
	if err := s.Global.Validate(); err != nil {
		return fmt.Errorf("global: %w", err)
	}
	for ns, p := range s.Namespaces {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("namespace %s: %w", ns, err)
		}
	}
	return nil
}

// Location is the settings' time zone.
func (s Settings) Location() *time.Location { return s.location() }

func (s Settings) location() *time.Location {
	if loc, err := time.LoadLocation(s.Timezone); err == nil && s.Timezone != "" {
		return loc
	}
	loc, _ := time.LoadLocation(DefaultTimezone)
	return loc
}

// Holiday returns the name of the holiday on a date ("" when none; a holiday without a name
// is "-").
func (s Settings) Holiday(date time.Time) (string, bool) {
	full, short := date.Format("2006-01-02"), date.Format("01-02")
	for _, h := range s.Holidays {
		if h.Date == full || h.Date == short {
			if h.Name == "" {
				return "-", true
			}
			return h.Name, true
		}
	}
	return "", false
}

// weekday is the day of the week a date counts as: Sunday on holidays.
func (s Settings) weekday(date time.Time) time.Weekday {
	if _, ok := s.Holiday(date); ok {
		return time.Sunday
	}
	return date.Weekday()
}

// Sources of a route's blocked hours.
const (
	SourceRoute     = "route"
	SourceNamespace = "namespace"
	SourceGlobal    = "global"
)

// Policy is the policy in force for a route, and where it comes from.
func (s Settings) Policy(r store.RouteConfig) (store.BlackoutPolicy, string) {
	if r.Blackout != nil && r.Blackout.Mode != "" {
		return *r.Blackout, SourceRoute
	}
	if p, ok := s.Namespaces[r.Namespace]; ok && p.Mode != "" {
		return p, SourceNamespace
	}
	if s.Global.Mode == "" {
		return store.BlackoutPolicy{Mode: store.BlackoutOff}, SourceGlobal
	}
	return s.Global, SourceGlobal
}

// InWindow reports whether t falls in one of the windows (in the settings' time zone).
func (s Settings) InWindow(windows []store.BlackoutWindow, t time.Time) bool {
	local := t.In(s.location())
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	at := local.Sub(day)
	weekday := s.weekday(day)
	for _, w := range windows {
		if w.Covers(weekday, at) {
			return true
		}
	}
	return false
}

// Blocked reports whether the route's application may not be woken at t: its blocked hours
// say so and neither an exception nor a pause lifts them. Always On routes are never blocked.
func (s Settings) Blocked(r store.RouteConfig, t time.Time) bool {
	p, _ := s.Policy(r)
	if p.Mode != store.BlackoutCustom || r.AlwaysOn || r.Paused(t) || r.ActiveException(t) != nil {
		return false
	}
	return s.InWindow(p.Windows, t)
}

// JustBlocked reports whether the route's blocked hours started in the last few minutes: its
// application is then put to sleep, without waiting for its idle timeout. Checking a few
// minutes back (rather than the previous check) works across leader changes.
func (s Settings) JustBlocked(r store.RouteConfig, now time.Time) bool {
	return s.Blocked(r, now) && !s.Blocked(r, now.Add(-JustBlockedWithin))
}

// JustBlockedWithin is how long after blocked hours start the application is still put to sleep.
const JustBlockedWithin = 3 * time.Minute

// State is what the dashboard shows of a route's blocked hours.
type State struct {
	Source  string               `json:"source"`
	Policy  store.BlackoutPolicy `json:"policy"`
	Blocked bool                 `json:"blocked"`
	// Exception is the exception in force, if any.
	Exception *store.Exception `json:"exception,omitempty"`
	// Change is when Blocked changes next (within a week; nil when it doesn't).
	Change *time.Time `json:"change,omitempty"`
	// Holiday is today's holiday, if any.
	Holiday string `json:"holiday,omitempty"`
}

// horizon is how far ahead the next change is looked for.
const horizon = 8 * 24 * time.Hour

// State describes the route's blocked hours at now.
func (s Settings) State(r store.RouteConfig, now time.Time) State {
	p, source := s.Policy(r)
	st := State{Source: source, Policy: p, Blocked: s.Blocked(r, now), Exception: r.ActiveException(now)}
	local := now.In(s.location())
	if name, ok := s.Holiday(local); ok {
		st.Holiday = name
	}
	if p.Mode == store.BlackoutCustom || len(r.Exceptions) > 0 {
		for _, t := range s.boundaries(r, p, now, now.Add(horizon)) {
			if s.Blocked(r, t) != st.Blocked {
				change := t
				st.Change = &change
				break
			}
		}
	}
	return st
}

// boundaries are the moments in (from, to] where the route's state may change: window starts
// and ends, midnights (holidays), exceptions and the end of a pause, in order.
func (s Settings) boundaries(r store.RouteConfig, p store.BlackoutPolicy, from, to time.Time) []time.Time {
	loc := s.location()
	local := from.In(loc)
	var out []time.Time
	add := func(t time.Time) {
		if t.After(from) && !t.After(to) {
			out = append(out, t)
		}
	}
	for d := -1; d <= int(to.Sub(from).Hours()/24)+1; d++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, loc)
		add(day)
		for _, w := range p.Windows {
			for _, b := range w.Boundaries() {
				add(time.Date(day.Year(), day.Month(), day.Day(), int(b.Hours()), int(b.Minutes())%60, 0, 0, loc))
			}
		}
	}
	for _, e := range r.Exceptions {
		add(e.From)
		add(e.To)
	}
	if r.Pause != nil && r.Pause.Until != nil {
		add(*r.Pause.Until)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Segment is a stretch of a day with the same state.
type Segment struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Blocked bool      `json:"blocked"`
	// Exception is the kind of exception lifting the blocked hours, if any (store.ExceptionAllow
	// or store.ExceptionOn).
	Exception string `json:"exception,omitempty"`
}

// Day is a day of the week ahead, for the dashboard's preview.
type Day struct {
	Date     string    `json:"date"` // YYYY-MM-DD
	Holiday  string    `json:"holiday,omitempty"`
	Segments []Segment `json:"segments"`
}

// Week is the route's state over the 7 days starting today, day by day.
func (s Settings) Week(r store.RouteConfig, now time.Time) []Day {
	p, _ := s.Policy(r)
	loc := s.location()
	local := now.In(loc)
	days := make([]Day, 0, 7)
	for d := 0; d < 7; d++ {
		start := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, loc)
		end := time.Date(local.Year(), local.Month(), local.Day()+d+1, 0, 0, 0, 0, loc)
		day := Day{Date: start.Format("2006-01-02"), Segments: []Segment{}}
		if name, ok := s.Holiday(start); ok {
			day.Holiday = name
		}
		cuts := append([]time.Time{start}, s.boundaries(r, p, start, end)...)
		if !cuts[len(cuts)-1].Equal(end) {
			cuts = append(cuts, end)
		}
		for i := 0; i+1 < len(cuts); i++ {
			if !cuts[i+1].After(cuts[i]) {
				continue
			}
			// The pause isn't shown: it's temporary, and the dashboard shows it elsewhere.
			at := cuts[i]
			unpaused := r
			unpaused.Pause = nil
			seg := Segment{From: at, To: cuts[i+1], Blocked: s.Blocked(unpaused, at)}
			if e := r.ActiveException(at); e != nil && p.Mode == store.BlackoutCustom && s.InWindow(p.Windows, at) {
				seg.Exception = e.Kind
			}
			if n := len(day.Segments); n > 0 && day.Segments[n-1].Blocked == seg.Blocked && day.Segments[n-1].Exception == seg.Exception {
				day.Segments[n-1].To = seg.To
				continue
			}
			day.Segments = append(day.Segments, seg)
		}
		days = append(days, day)
	}
	return days
}

// Describe renders windows compactly for logs: "mon,tue 22:00-06:00; sat,sun 00:00-24:00".
func Describe(windows []store.BlackoutWindow) string {
	parts := make([]string, len(windows))
	for i, w := range windows {
		days := "every day"
		if len(w.Days) > 0 {
			days = strings.Join(w.Days, ",")
		}
		parts[i] = fmt.Sprintf("%s %s-%s", days, w.From, w.To)
	}
	return strings.Join(parts, "; ")
}
