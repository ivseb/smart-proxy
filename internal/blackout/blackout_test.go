package blackout

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"smart-proxy/internal/store"
)

var rome, _ = time.LoadLocation("Europe/Rome")

// at is a moment in Rome: at(2026, 10, 16, 23, 30) is Friday 16 October 2026, 23:30.
func at(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, rome) }

// nightsAndWeekends: weeknights 22:00-06:00, and the whole weekend.
var nightsAndWeekends = store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{
	{Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "22:00", To: "06:00"},
	{Days: []string{"sat", "sun"}, From: "00:00", To: "24:00"},
}}

func settings() Settings {
	s := Defaults()
	s.Global = nightsAndWeekends
	s.Holidays = []Holiday{{Date: "12-25", Name: "Christmas"}, {Date: "2026-11-02", Name: "Once"}}
	return s
}

func TestBlockedHours(t *testing.T) {
	s := settings()
	r := store.RouteConfig{Namespace: "shop"}
	for _, c := range []struct {
		when    time.Time
		blocked bool
	}{
		{at(2026, 10, 14, 12, 0), false}, // Wednesday noon
		{at(2026, 10, 14, 21, 59), false},
		{at(2026, 10, 14, 22, 0), true},
		{at(2026, 10, 15, 5, 59), true}, // Thursday morning: Wednesday's window
		{at(2026, 10, 15, 6, 0), false},
		{at(2026, 10, 16, 23, 0), true},  // Friday night
		{at(2026, 10, 17, 12, 0), true},  // Saturday
		{at(2026, 10, 18, 23, 59), true}, // Sunday night
		{at(2026, 10, 19, 5, 0), true},   // Monday morning: Monday's night hours
		{at(2026, 10, 19, 6, 0), false},
		{at(2026, 12, 25, 12, 0), true}, // Christmas (a Friday): a Sunday
		{at(2026, 12, 24, 12, 0), false},
		{at(2026, 11, 2, 12, 0), true}, // A one-off holiday (a Monday)
		{at(2027, 11, 2, 12, 0), false},
	} {
		if got := s.Blocked(r, c.when); got != c.blocked {
			t.Errorf("%s: blocked %v, want %v", c.when.Format("Mon 2006-01-02 15:04"), got, c.blocked)
		}
	}
}

func TestPolicyLevels(t *testing.T) {
	s := settings()
	s.Namespaces["tools"] = store.BlackoutPolicy{Mode: store.BlackoutOff}
	s.Namespaces["batch"] = store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{{From: "08:00", To: "18:00"}}}
	noon := at(2026, 10, 14, 12, 0)
	night := at(2026, 10, 14, 23, 0)
	for _, c := range []struct {
		route   store.RouteConfig
		source  string
		blocked [2]bool // at noon, at night
	}{
		{store.RouteConfig{Namespace: "shop"}, SourceGlobal, [2]bool{false, true}},
		{store.RouteConfig{Namespace: "tools"}, SourceNamespace, [2]bool{false, false}},
		{store.RouteConfig{Namespace: "batch"}, SourceNamespace, [2]bool{true, false}},
		{store.RouteConfig{Namespace: "batch", Blackout: &store.BlackoutPolicy{Mode: store.BlackoutOff}}, SourceRoute, [2]bool{false, false}},
		{store.RouteConfig{Namespace: "tools", Blackout: &nightsAndWeekends}, SourceRoute, [2]bool{false, true}},
	} {
		_, source := s.Policy(c.route)
		got := [2]bool{s.Blocked(c.route, noon), s.Blocked(c.route, night)}
		if source != c.source || got != c.blocked {
			t.Errorf("%s/%v: %s %v, want %s %v", c.route.Namespace, c.route.Blackout, source, got, c.source, c.blocked)
		}
	}
}

func TestExceptionsAndPausesLiftTheBlock(t *testing.T) {
	s := settings()
	saturday := at(2026, 10, 17, 12, 0)
	r := store.RouteConfig{Exceptions: []store.Exception{{Kind: store.ExceptionAllow, From: at(2026, 10, 17, 8, 0), To: at(2026, 10, 17, 20, 0)}}}
	if s.Blocked(r, saturday) || !s.Blocked(r, at(2026, 10, 17, 20, 0)) || r.KeptOn(saturday) {
		t.Fatal("an allowing exception lifts the block for its time only, without keeping the app on")
	}
	r.Exceptions[0].Kind = store.ExceptionOn
	if s.Blocked(r, saturday) || !r.KeptOn(saturday) {
		t.Fatal("an exception keeping the app on lifts the block")
	}
	until := at(2026, 10, 17, 14, 0)
	paused := store.RouteConfig{Pause: &store.Pause{Until: &until}}
	if s.Blocked(paused, saturday) || !s.Blocked(paused, until) {
		t.Fatal("a pause lifts the block while it lasts")
	}
}

func TestJustBlocked(t *testing.T) {
	s := settings()
	r := store.RouteConfig{}
	if !s.JustBlocked(r, at(2026, 10, 14, 22, 1)) || s.JustBlocked(r, at(2026, 10, 14, 22, 10)) || s.JustBlocked(r, at(2026, 10, 14, 21, 59)) {
		t.Fatal("blocked hours just started at 22:00")
	}
	// The end of an exception inside blocked hours counts as a start.
	r.Exceptions = []store.Exception{{Kind: store.ExceptionOn, From: at(2026, 10, 17, 8, 0), To: at(2026, 10, 17, 20, 0)}}
	if !s.JustBlocked(r, at(2026, 10, 17, 20, 1)) {
		t.Fatal("the exception's end starts the block again")
	}
}

func TestStateSaysWhenItChanges(t *testing.T) {
	s := settings()
	r := store.RouteConfig{}
	st := s.State(r, at(2026, 10, 14, 23, 0)) // Wednesday night
	if !st.Blocked || st.Source != SourceGlobal || st.Change == nil || !st.Change.Equal(at(2026, 10, 15, 6, 0)) {
		t.Fatalf("Wednesday night: %+v", st)
	}
	st = s.State(r, at(2026, 10, 16, 12, 0)) // Friday noon: blocked from 22:00 until Monday 06:00
	if st.Blocked || st.Change == nil || !st.Change.Equal(at(2026, 10, 16, 22, 0)) {
		t.Fatalf("Friday noon: %+v", st)
	}
	st = s.State(r, at(2026, 12, 25, 12, 0))
	if st.Holiday != "Christmas" || !st.Blocked {
		t.Fatalf("Christmas: %+v", st)
	}
	if st := s.State(store.RouteConfig{Blackout: &store.BlackoutPolicy{Mode: store.BlackoutOff}}, at(2026, 10, 17, 12, 0)); st.Blocked || st.Change != nil {
		t.Fatalf("no blocked hours: %+v", st)
	}
}

func TestWeekPreview(t *testing.T) {
	s := settings()
	r := store.RouteConfig{Exceptions: []store.Exception{{Kind: store.ExceptionAllow, From: at(2026, 10, 17, 8, 0), To: at(2026, 10, 17, 20, 0)}}}
	week := s.Week(r, at(2026, 10, 14, 12, 0)) // From Wednesday
	if len(week) != 7 || week[0].Date != "2026-10-14" {
		t.Fatalf("week: %+v", week)
	}
	wed := week[0].Segments
	if len(wed) != 3 || wed[0].Blocked != true || !wed[0].To.Equal(at(2026, 10, 14, 6, 0)) || wed[1].Blocked || !wed[2].From.Equal(at(2026, 10, 14, 22, 0)) {
		t.Fatalf("Wednesday: %+v", wed)
	}
	sat := week[3].Segments
	if len(sat) != 3 || !sat[0].Blocked || sat[1].Blocked || sat[1].Exception != store.ExceptionAllow || !sat[2].Blocked {
		t.Fatalf("Saturday with an exception: %+v", sat)
	}
}

func TestDaylightSavingTime(t *testing.T) {
	// 25 October 2026: clocks go back in Rome (03:00 -> 02:00). A Sunday, blocked all day.
	s := settings()
	s.Global = store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{{From: "01:00", To: "04:00"}}}
	sunday := s.Week(store.RouteConfig{}, at(2026, 10, 25, 0, 0))[0]
	if len(sunday.Segments) != 3 || sunday.Segments[1].To.Sub(sunday.Segments[1].From) != 4*time.Hour {
		t.Fatalf("01:00-04:00 lasts 4 hours on the day clocks go back: %+v", sunday.Segments)
	}
}

func TestValidate(t *testing.T) {
	for name, s := range map[string]Settings{
		"timezone": {Timezone: "Mars/Olympus", Global: store.BlackoutPolicy{Mode: store.BlackoutOff}},
		"holiday":  {Timezone: "UTC", Holidays: []Holiday{{Date: "12/25"}}, Global: store.BlackoutPolicy{Mode: store.BlackoutOff}},
		"no date":  {Timezone: "UTC", Holidays: []Holiday{{Date: "02-30"}}, Global: store.BlackoutPolicy{Mode: store.BlackoutOff}},
		"window":   {Timezone: "UTC", Global: store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{{From: "22:00", To: "22:00"}}}},
		"empty":    {Timezone: "UTC", Global: store.BlackoutPolicy{Mode: store.BlackoutCustom}},
		"day":      {Timezone: "UTC", Global: store.BlackoutPolicy{Mode: store.BlackoutCustom, Windows: []store.BlackoutWindow{{Days: []string{"lun"}, From: "22:00", To: "06:00"}}}},
	} {
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := settings().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestKeeperSharesTheSettings(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	a := &Keeper{Client: client, Namespace: "sp", Name: "sp-settings"}
	b := &Keeper{Client: client, Namespace: "sp", Name: "sp-settings"}
	if err := a.Load(ctx); err != nil || a.Get().Global.Mode != store.BlackoutOff || a.Get().Timezone != DefaultTimezone {
		t.Fatalf("defaults: %+v %v", a.Get(), err)
	}
	if err := a.Save(ctx, settings()); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx, Settings{Timezone: "Nowhere"}); err == nil {
		t.Fatal("invalid settings saved")
	}
	cm, _ := client.CoreV1().ConfigMaps("sp").Get(ctx, "sp-settings", metav1.GetOptions{})
	b.Adopt(cm)
	if b.Get().Global.Mode != store.BlackoutCustom || len(b.Get().Holidays) != 2 {
		t.Fatalf("adopted: %+v", b.Get())
	}
}
