package declarative

import (
	"reflect"
	"testing"
	"time"

	"smart-proxy/internal/store"
)

func TestParse(t *testing.T) {
	s, err := Parse(map[string]string{
		Enabled:      "true",
		IdleTimeout:  "45m",
		Dependencies: "api, statefulset/db:keep",
		StartInOrder: "true",
		Schedule:     "mon-fri 08:00-19:00 Europe/Rome",
		Workload:     "statefulset/web",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		IdleTimeout:  45 * time.Minute,
		Dependencies: []store.DependencyConfig{{Name: "api", StopOnIdle: true}, {Name: "statefulset/db", StopOnIdle: false}},
		StartInOrder: true,
		Schedule:     &store.Schedule{Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "08:00", To: "19:00", Timezone: "Europe/Rome"},
		Workload:     "statefulset/web",
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
}

func TestParseErrors(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"bad timeout":  {IdleTimeout: "soon"},
		"bad bool":     {AlwaysOn: "yes please"},
		"bad schedule": {Schedule: "weekdays 9-5"},
		"bad day":      {Schedule: "mon-funday 08:00-09:00"},
		"bad tz":       {Schedule: "daily 08:00-09:00 Mars/Base"},
		"bad source":   {IgnoreSources: "10.0.0.0/99"},
		"bad asleep":   {WhenAsleep: "snooze"},
	} {
		if _, err := Parse(annotations); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseScheduleDays(t *testing.T) {
	for in, want := range map[string][]string{
		"daily 08:00-09:00":       nil,
		"mon,wed,fri 08:00-09:00": {"mon", "wed", "fri"},
		"fri-mon 08:00-09:00":     {"fri", "sat", "sun", "mon"},
	} {
		s, err := ParseSchedule(in)
		if err != nil || !reflect.DeepEqual(s.Days, want) {
			t.Errorf("%q: days=%v err=%v, want %v", in, s.Days, err, want)
		}
	}
}

func TestIsEnabled(t *testing.T) {
	if IsEnabled(map[string]string{}) || IsEnabled(map[string]string{Enabled: "false"}) || !IsEnabled(map[string]string{Enabled: "True"}) {
		t.Fatal("IsEnabled")
	}
}

func TestParseTrafficAnnotations(t *testing.T) {
	s, err := Parse(map[string]string{
		IgnoreUserAgents: "MyMonitor, internal-checker",
		IgnorePaths:      "/healthz\n/status/*",
		IgnoreMethods:    "HEAD",
		WhenAsleep:       "Unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Ignore == nil || len(s.Ignore.UserAgents) != 2 || len(s.Ignore.Paths) != 2 || s.Ignore.Methods[0] != "HEAD" || s.WhenAsleep != "unavailable" {
		t.Fatalf("settings = %+v %+v", s, s.Ignore)
	}
}
