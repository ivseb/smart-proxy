package store

import (
	"fmt"
	"strings"
	"time"
)

// BlackoutWindow is a stretch of the week when sleeping applications must not be woken: on the
// given days (none: every day), the hours from From to To ("HH:MM"). A To earlier than From
// means the night hours of those days: Mon-Fri 22:00-06:00 is 00:00-06:00 and 22:00-24:00 of
// each weekday (so Monday morning is blocked too). 00:00-24:00 is the whole day.
type BlackoutWindow struct {
	Days []string `json:"days,omitempty"`
	From string   `json:"from"`
	To   string   `json:"to"`
}

// Blackout modes: a route or namespace with its own windows, or none at all. A route or
// namespace without a policy follows the level above (namespace, then global).
const (
	BlackoutCustom = "custom"
	BlackoutOff    = "off"
)

// BlackoutPolicy says when a route's application may not be woken.
type BlackoutPolicy struct {
	Mode    string           `json:"mode"` // BlackoutCustom or BlackoutOff
	Windows []BlackoutWindow `json:"windows,omitempty"`
}

// Validate reports an invalid policy.
func (p BlackoutPolicy) Validate() error {
	switch p.Mode {
	case BlackoutOff:
		return nil
	case BlackoutCustom:
		if len(p.Windows) == 0 {
			return fmt.Errorf("blocked hours need at least one window")
		}
		for _, w := range p.Windows {
			if err := w.Validate(); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("blackout mode must be %q or %q", BlackoutCustom, BlackoutOff)
}

// Validate reports a malformed window.
func (w BlackoutWindow) Validate() error {
	for _, d := range w.Days {
		if _, ok := weekdays[strings.ToLower(d)]; !ok {
			return fmt.Errorf("invalid day %q (use mon, tue, wed, thu, fri, sat, sun)", d)
		}
	}
	from, err := parseClock24(w.From)
	if err != nil || from == 24*time.Hour {
		return fmt.Errorf("invalid from %q (HH:MM)", w.From)
	}
	to, err := parseClock24(w.To)
	if err != nil {
		return fmt.Errorf("invalid to %q (HH:MM, or 24:00)", w.To)
	}
	if from == to {
		return fmt.Errorf("from and to must differ (00:00-24:00 is the whole day)")
	}
	return nil
}

// Covers reports whether a time of day of a day of the week falls in the window.
func (w BlackoutWindow) Covers(day time.Weekday, at time.Duration) bool {
	from, err1 := parseClock24(w.From)
	to, err2 := parseClock24(w.To)
	if err1 != nil || err2 != nil || from == to || !w.onDay(day) {
		return false
	}
	if from < to {
		return at >= from && at < to
	}
	return at >= from || at < to // Night hours: the evening and the morning of the same day
}

func (w BlackoutWindow) onDay(day time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, d := range w.Days {
		if weekdays[strings.ToLower(d)] == day {
			return true
		}
	}
	return false
}

// Boundaries are the times of day the window starts and ends.
func (w BlackoutWindow) Boundaries() []time.Duration {
	from, err1 := parseClock24(w.From)
	to, err2 := parseClock24(w.To)
	if err1 != nil || err2 != nil {
		return nil
	}
	return []time.Duration{from, to % (24 * time.Hour)}
}

func parseClock24(v string) (time.Duration, error) {
	if v == "24:00" {
		return 24 * time.Hour, nil
	}
	return parseClock(v)
}

// Exception kinds: the application may be woken as usual, or is kept awake.
const (
	ExceptionAllow = "allow"
	ExceptionOn    = "on"
)

// Exception lifts a route's blocked hours for a while: ExceptionAllow lets requests wake the
// application as usual, ExceptionOn also keeps it awake for the whole time.
type Exception struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at"`
}

// Active reports whether the exception applies at t.
func (e Exception) Active(t time.Time) bool { return !t.Before(e.From) && t.Before(e.To) }

// ActiveException is the route's exception in force at t (one keeping it awake first), or nil.
func (r RouteConfig) ActiveException(t time.Time) *Exception {
	var found *Exception
	for i, e := range r.Exceptions {
		if e.Active(t) && (found == nil || e.Kind == ExceptionOn) {
			found = &r.Exceptions[i]
		}
	}
	return found
}

// KeptOn reports whether an exception keeps the route awake at t.
func (r RouteConfig) KeptOn(t time.Time) bool {
	e := r.ActiveException(t)
	return e != nil && e.Kind == ExceptionOn
}
