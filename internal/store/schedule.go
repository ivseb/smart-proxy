package store

import (
	"fmt"
	"strings"
	"time"
)

// Schedule keeps a route awake during given hours, e.g. office hours on weekdays.
// Outside the window the usual idle timeout applies.
type Schedule struct {
	// Days the window starts on: "mon".."sun". Empty means every day.
	Days []string `json:"days,omitempty"`
	// From and To are "HH:MM". A To earlier than From spans midnight (e.g. 22:00-06:00).
	From string `json:"from"`
	To   string `json:"to"`
	// Timezone is an IANA name such as "Europe/Rome". Empty means UTC.
	Timezone string `json:"timezone,omitempty"`
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Validate reports a malformed schedule.
func (s Schedule) Validate() error {
	for _, d := range s.Days {
		if _, ok := weekdays[strings.ToLower(d)]; !ok {
			return fmt.Errorf("invalid day %q (use mon, tue, wed, thu, fri, sat, sun)", d)
		}
	}
	from, err := parseClock(s.From)
	if err != nil {
		return fmt.Errorf("invalid from: %w", err)
	}
	to, err := parseClock(s.To)
	if err != nil {
		return fmt.Errorf("invalid to: %w", err)
	}
	if from == to {
		return fmt.Errorf("from and to must differ")
	}
	if _, err := s.location(); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}
	return nil
}

// Active reports whether t falls inside the schedule's window.
func (s Schedule) Active(t time.Time) bool {
	loc, err := s.location()
	if err != nil {
		return false
	}
	from, err1 := parseClock(s.From)
	to, err2 := parseClock(s.To)
	if err1 != nil || err2 != nil || from == to {
		return false
	}
	local := t.In(loc)
	now := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second

	if from < to {
		return s.onDay(local.Weekday()) && now >= from && now < to
	}
	// Spans midnight: the evening part belongs to today's window, the morning part to yesterday's.
	if now >= from {
		return s.onDay(local.Weekday())
	}
	return now < to && s.onDay((local.Weekday()+6)%7)
}

func (s Schedule) onDay(day time.Weekday) bool {
	if len(s.Days) == 0 {
		return true
	}
	for _, d := range s.Days {
		if weekdays[strings.ToLower(d)] == day {
			return true
		}
	}
	return false
}

func (s Schedule) location() (*time.Location, error) {
	if s.Timezone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(s.Timezone)
}

func parseClock(v string) (time.Duration, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", v)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// ScheduledAwake reports whether the route's schedule keeps it awake at t.
func (r RouteConfig) ScheduledAwake(t time.Time) bool {
	return r.Schedule != nil && r.Schedule.Active(t)
}
