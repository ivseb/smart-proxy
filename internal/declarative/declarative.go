// Package declarative reads route settings from annotations on Ingresses and Routes, so they
// can live in Git next to the application instead of being set in the dashboard:
//
//	smart-proxy/enabled: "true"                     # opt in (required)
//	smart-proxy/idle-timeout: 45m                   # default 30m
//	smart-proxy/dependencies: api, statefulset/db:keep   # ":keep" = don't sleep it with the app
//	smart-proxy/start-in-order: "true"
//	smart-proxy/always-on: "true"
//	smart-proxy/schedule: mon-fri 08:00-19:00 Europe/Rome
//	smart-proxy/workload: statefulset/web           # when it can't be inferred from the Service
//	smart-proxy/badge: "true"
//	smart-proxy/ignore-user-agents: MyMonitor, internal-checker   # on top of the global list
//	smart-proxy/ignore-paths: /healthz, /status/*
//	smart-proxy/ignore-sources: 10.20.0.0/16
//	smart-proxy/ignore-methods: HEAD
//	smart-proxy/when-asleep: respond                # or unavailable, wake
//	smart-proxy/managed-backends: web-v1            # Route balancing Services: the ones to sleep/wake
package declarative

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/traffic"
)

// Annotation keys users set.
const (
	Enabled      = "smart-proxy/enabled"
	IdleTimeout  = "smart-proxy/idle-timeout"
	Dependencies = "smart-proxy/dependencies"
	StartInOrder = "smart-proxy/start-in-order"
	AlwaysOn     = "smart-proxy/always-on"
	Schedule     = "smart-proxy/schedule"
	Workload     = "smart-proxy/workload"
	Badge        = "smart-proxy/badge"

	IgnoreUserAgents = "smart-proxy/ignore-user-agents"
	IgnorePaths      = "smart-proxy/ignore-paths"
	IgnoreSources    = "smart-proxy/ignore-sources"
	IgnoreMethods    = "smart-proxy/ignore-methods"
	WhenAsleep       = "smart-proxy/when-asleep"
	ManagedBackends  = "smart-proxy/managed-backends"
)

// IsEnabled reports whether the resource opts in.
func IsEnabled(annotations map[string]string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(annotations[Enabled]))
	return v
}

// Settings are the route settings the annotations define.
type Settings struct {
	IdleTimeout  time.Duration
	Dependencies []store.DependencyConfig
	StartInOrder bool
	AlwaysOn     bool
	Schedule     *store.Schedule
	Workload     string // Empty: infer from the Service
	InjectBadge  bool
	Ignore       *traffic.Rules
	WhenAsleep   string
	// ManagedBackends are the Services of a balanced Route that sleep and wake with it
	// (nil: those running when it was first patched).
	ManagedBackends []string
}

// Parse reads the settings from a resource's annotations.
func Parse(annotations map[string]string) (Settings, error) {
	var s Settings
	var err error
	get := func(key string) string { return strings.TrimSpace(annotations[key]) }
	boolean := func(key string) bool {
		if err != nil || get(key) == "" {
			return false
		}
		v, perr := strconv.ParseBool(get(key))
		if perr != nil {
			err = fmt.Errorf("%s: %q is not true/false", key, get(key))
		}
		return v
	}

	if v := get(IdleTimeout); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil || d <= 0 {
			return s, fmt.Errorf("%s: %q is not a duration like 30m", IdleTimeout, v)
		}
		s.IdleTimeout = d
	}
	for _, item := range strings.Split(get(Dependencies), ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		name, keep := strings.CutSuffix(item, ":keep")
		s.Dependencies = append(s.Dependencies, store.DependencyConfig{Name: strings.TrimSpace(name), StopOnIdle: !keep})
	}
	if v := get(Schedule); v != "" {
		sched, perr := ParseSchedule(v)
		if perr != nil {
			return s, fmt.Errorf("%s: %w", Schedule, perr)
		}
		s.Schedule = sched
	}
	s.StartInOrder = boolean(StartInOrder)
	s.AlwaysOn = boolean(AlwaysOn)
	s.InjectBadge = boolean(Badge)
	s.Workload = get(Workload)

	rules := traffic.Rules{
		UserAgents: traffic.SplitList(get(IgnoreUserAgents)),
		Paths:      traffic.SplitList(get(IgnorePaths)),
		Sources:    traffic.SplitList(get(IgnoreSources)),
		Methods:    traffic.SplitList(get(IgnoreMethods)),
	}
	if !rules.IsZero() {
		if verr := rules.Validate(); verr != nil {
			return s, fmt.Errorf("smart-proxy/ignore-*: %w", verr)
		}
		s.Ignore = &rules
	}
	s.ManagedBackends = traffic.SplitList(get(ManagedBackends))
	s.WhenAsleep = strings.ToLower(get(WhenAsleep))
	if verr := (store.RouteConfig{WhenAsleep: s.WhenAsleep}).ValidateTraffic(); verr != nil {
		return s, fmt.Errorf("%s: %w", WhenAsleep, verr)
	}
	return s, err
}

var dayIndex = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
var dayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ParseSchedule reads "<days> <from>-<to> [timezone]", e.g. "mon-fri 08:00-19:00 Europe/Rome",
// "mon,wed,fri 09:00-12:00" or "daily 22:00-06:00 UTC".
func ParseSchedule(v string) (*store.Schedule, error) {
	fields := strings.Fields(v)
	if len(fields) < 2 || len(fields) > 3 {
		return nil, fmt.Errorf("%q: want \"<days> <from>-<to> [timezone]\", e.g. \"mon-fri 08:00-19:00 Europe/Rome\"", v)
	}
	days, err := parseDays(strings.ToLower(fields[0]))
	if err != nil {
		return nil, err
	}
	from, to, ok := strings.Cut(fields[1], "-")
	if !ok {
		return nil, fmt.Errorf("%q: hours must look like 08:00-19:00", fields[1])
	}
	s := &store.Schedule{Days: days, From: from, To: to}
	if len(fields) == 3 {
		s.Timezone = fields[2]
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func parseDays(v string) ([]string, error) {
	if v == "daily" || v == "*" || v == "every-day" {
		return nil, nil
	}
	var days []string
	for _, part := range strings.Split(v, ",") {
		first, last, isRange := strings.Cut(part, "-")
		i, ok1 := dayIndex[first]
		if !isRange {
			if !ok1 {
				return nil, fmt.Errorf("unknown day %q", part)
			}
			days = append(days, first)
			continue
		}
		j, ok2 := dayIndex[last]
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("unknown day range %q", part)
		}
		for k := i; ; k = (k + 1) % 7 { // Ranges may wrap: fri-mon
			days = append(days, dayNames[k])
			if k == j {
				break
			}
		}
	}
	return days, nil
}

// Apply sets the declared settings on a route configuration.
func (s Settings) Apply(r *store.RouteConfig) {
	r.IdleTimeout = s.IdleTimeout
	if r.IdleTimeout == 0 {
		r.IdleTimeout = store.DefaultIdleTimeout
	}
	r.Dependencies = s.Dependencies
	if r.Dependencies == nil {
		r.Dependencies = []store.DependencyConfig{}
	}
	r.StartInOrder = s.StartInOrder
	r.AlwaysOn = s.AlwaysOn
	r.Schedule = s.Schedule
	r.InjectBadge = s.InjectBadge
	r.Ignore = s.Ignore
	r.WhenAsleep = s.WhenAsleep
	if s.Workload != "" {
		r.Deployment = s.Workload
	}
}
