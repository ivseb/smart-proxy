package uptime

import (
	"testing"
	"time"
)

func TestSampleCountsUpReplicasAndWakes(t *testing.T) {
	r := NewRecorder()
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	replicas := []int32{0, 0, 2, 2, 0, 1} // Asleep, woken (2 replicas), asleep, woken again
	for i, n := range replicas {
		r.Sample(start.Add(time.Duration(i)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: n}})
	}
	series := r.Snapshot(start)
	if len(series) != 1 {
		t.Fatalf("series: %+v", series)
	}
	s := series[0]
	if s.Seen[0] != 6 || s.Up[0] != 3 || s.Replicas[0] != 5 || s.Wakes[0] != 2 {
		t.Fatalf("seen %d up %d replicas %d wakes %d", s.Seen[0], s.Up[0], s.Replicas[0], s.Wakes[0])
	}
}

func TestFirstSampleUpIsNotAWake(t *testing.T) {
	r := NewRecorder()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	r.Sample(now, []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	if s := r.Snapshot(now)[0]; s.Wakes[0] != 0 {
		t.Fatalf("wakes %d", s.Wakes[0])
	}
}

func TestOnlyTheLastThirtyDaysAreKept(t *testing.T) {
	r := NewRecorder()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r.Sample(start, []State{{Namespace: "shop", Workload: "old", Replicas: 1}})
	later := start.Add(40 * 24 * time.Hour)
	r.Sample(later, []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	r.Sample(start.Add(5*24*time.Hour), []State{{Namespace: "shop", Workload: "web", Replicas: 1}}) // Before the series: ignored
	series := r.Snapshot(later)
	if len(series) != 1 || series[0].Workload != "web" {
		t.Fatalf("workloads not seen for 30 days aren't dropped: %+v", series)
	}

	// A series sampled for longer than the window keeps only the window.
	r.Sample(later.Add(31*24*time.Hour), []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	if s := r.Snapshot(later.Add(31 * 24 * time.Hour))[0]; len(s.Seen) != window || s.Start != later.Add(31*24*time.Hour).Unix()/3600-window+1 {
		t.Fatalf("len %d start %d", len(s.Seen), s.Start)
	}
}

func TestSummarizeByDayInATimeZone(t *testing.T) {
	rome, _ := time.LoadLocation("Europe/Rome")
	r := NewRecorder()
	// 23:30 UTC on Oct 1st is 01:30 on Oct 2nd in Rome: 30 minutes up on each side of midnight UTC.
	at := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		r.Sample(at.Add(time.Duration(i)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	from, to := LastDays(now, 7, rome)
	rep := Summarize(r.Snapshot(now)[0], from, to, rome, Options{Hours: true})
	if len(rep.Days) != 7 || rep.Days[6].Date != "2026-10-03" {
		t.Fatalf("days %+v", rep.Days)
	}
	if rep.Days[5].Up != 60 || rep.Days[4].Up != 0 || rep.Up != 60 || rep.Observed != 60 {
		t.Fatalf("Oct 2nd in Rome should have the 60 minutes: %+v", rep)
	}
	if len(rep.Hours) != 2 || rep.Since == nil || !rep.Since.Equal(time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("hours %+v since %v", rep.Hours, rep.Since)
	}
	from, to = LastDays(now, 1, rome)
	if short := Summarize(r.Snapshot(now)[0], from, to, rome, Options{}); short.Observed != 0 || short.Hours != nil {
		t.Fatalf("today only: %+v", short)
	}
}

func TestReplaceKeepsTheHistory(t *testing.T) {
	r := NewRecorder()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	r.Sample(now, []State{{Namespace: "shop", Workload: "web", Replicas: 0}})
	other := NewRecorder()
	other.Replace(r.Snapshot(now))
	if other.TakeDirty() {
		t.Fatal("a loaded history isn't dirty")
	}
	other.Sample(now.Add(time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	if s := other.Snapshot(now)[0]; s.Seen[0] != 2 || s.Wakes[0] != 1 || !other.TakeDirty() {
		t.Fatalf("continued history: %+v", s)
	}
}

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{"web": "web", "deployment/web": "web", "sts/db": "statefulset/db", "StatefulSet/db": "statefulset/db", "dc/legacy": "deploymentconfig/legacy"} {
		if got := Canonical(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestChangesAreKeptToTheMinute(t *testing.T) {
	r := NewRecorder()
	night := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC) // 22:00 in Rome
	sample := func(minute int, n int32) {
		r.Sample(night.Add(time.Duration(minute)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: n}})
	}
	for m := 0; m < 10; m++ {
		sample(m, 1) // Up until 22:10
	}
	for m := 10; m < 60*9; m++ {
		n := int32(0)
		if m >= 312 && m < 341 { // Woken at 03:12, asleep again at 03:41
			n = 2
		}
		if m >= 100 && m < 103 { // Smart Proxy not running for 3 minutes, 23:40-23:43
			continue
		}
		sample(m, n)
	}
	s := r.Snapshot(night)[0]
	want := []Change{{At: night.Unix(), Replicas: 1, Gap: true}, {At: night.Add(10 * time.Minute).Unix()},
		{At: night.Add(103 * time.Minute).Unix(), Gap: true, GapFrom: night.Add(99 * time.Minute).Unix()}, {At: night.Add(312 * time.Minute).Unix(), Replicas: 2}, {At: night.Add(341 * time.Minute).Unix()}}
	if len(s.Changes) != len(want) {
		t.Fatalf("changes %+v", s.Changes)
	}
	for i := range want {
		if s.Changes[i] != want[i] {
			t.Fatalf("change %d: %+v, want %+v", i, s.Changes[i], want[i])
		}
	}

	// The night from 23:00 to 07:00 in Rome: asleep at its start, woken once.
	rome, _ := time.LoadLocation("Europe/Rome")
	from := time.Date(2026, 10, 1, 23, 0, 0, 0, rome)
	rep := Summarize(s, from, from.Add(8*time.Hour), rome, Options{Hours: true, Changes: true})
	if rep.Before == nil || rep.Before.Replicas != 0 || len(rep.Changes) != 3 || rep.Changes[1].Replicas != 2 {
		t.Fatalf("before %+v changes %+v", rep.Before, rep.Changes)
	}
	if rep.Up != 29 || rep.Wakes != 1 || len(rep.Hours) != 8 || len(rep.Days) != 2 {
		t.Fatalf("up %d wakes %d hours %d days %+v", rep.Up, rep.Wakes, len(rep.Hours), rep.Days)
	}
}

func TestChangesBeyondTheWindowAreDropped(t *testing.T) {
	r := NewRecorder()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < 40; d++ {
		at := start.Add(time.Duration(d) * 24 * time.Hour)
		r.Sample(at, []State{{Namespace: "shop", Workload: "web", Replicas: int32(d % 2)}})
		r.Sample(at.Add(time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: int32(d % 2)}})
	}
	s := r.Snapshot(start.Add(40 * 24 * time.Hour))[0]
	// The latest change before the window is kept: the state at its start.
	if len(s.Changes) < 30 || len(s.Changes) > 32 || s.Changes[1].At < s.Start*3600 || s.Changes[0].At >= s.Start*3600 {
		t.Fatalf("%d changes, first %v, window from %v", len(s.Changes), time.Unix(s.Changes[0].At, 0), time.Unix(s.Start*3600, 0))
	}
}
