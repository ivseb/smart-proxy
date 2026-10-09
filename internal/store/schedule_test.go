package store

import (
	"testing"
	"time"
)

func at(t *testing.T, value, tz string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestScheduleActive(t *testing.T) {
	office := Schedule{Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "08:00", To: "19:00", Timezone: "Europe/Rome"}
	overnight := Schedule{Days: []string{"fri"}, From: "22:00", To: "06:00"} // UTC
	cases := []struct {
		name     string
		schedule Schedule
		t        time.Time
		want     bool
	}{
		{"weekday morning", office, at(t, "2026-10-07 08:00", "Europe/Rome"), true}, // Wednesday
		{"weekday before", office, at(t, "2026-10-07 07:59", "Europe/Rome"), false},
		{"weekday end is exclusive", office, at(t, "2026-10-07 19:00", "Europe/Rome"), false},
		{"saturday", office, at(t, "2026-10-10 10:00", "Europe/Rome"), false},
		{"other timezone", office, at(t, "2026-10-07 06:30", "UTC"), true},                   // 08:30 in Rome (CEST)
		{"overnight evening", overnight, at(t, "2026-10-09 23:00", "UTC"), true},             // Friday
		{"overnight morning after", overnight, at(t, "2026-10-10 05:00", "UTC"), true},       // Saturday, Friday's window
		{"overnight morning of the day", overnight, at(t, "2026-10-09 05:00", "UTC"), false}, // Friday morning: Thursday's window
		{"every day", Schedule{From: "00:00", To: "01:00"}, at(t, "2026-10-11 00:30", "UTC"), true},
	}
	for _, tc := range cases {
		if got := tc.schedule.Active(tc.t); got != tc.want {
			t.Errorf("%s: Active = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestScheduleValidate(t *testing.T) {
	valid := Schedule{Days: []string{"Mon"}, From: "08:00", To: "18:30", Timezone: "America/New_York"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid schedule: %v", err)
	}
	for name, s := range map[string]Schedule{
		"bad day":      {Days: []string{"funday"}, From: "08:00", To: "09:00"},
		"bad time":     {From: "8am", To: "09:00"},
		"empty window": {From: "08:00", To: "08:00"},
		"bad timezone": {From: "08:00", To: "09:00", Timezone: "Mars/Olympus"},
	} {
		if s.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
