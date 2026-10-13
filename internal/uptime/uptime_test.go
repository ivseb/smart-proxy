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
	rep := Summarize(r.Snapshot(now)[0], now, 7, rome, true)
	if len(rep.Days) != 7 || rep.Days[6].Date != "2026-10-03" {
		t.Fatalf("days %+v", rep.Days)
	}
	if rep.Days[5].Up != 60 || rep.Days[4].Up != 0 || rep.Up != 60 || rep.Observed != 60 {
		t.Fatalf("Oct 2nd in Rome should have the 60 minutes: %+v", rep)
	}
	if len(rep.Hours) != 2 || rep.Since == nil || !rep.Since.Equal(time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("hours %+v since %v", rep.Hours, rep.Since)
	}
	if short := Summarize(r.Snapshot(now)[0], now, 1, rome, false); short.Observed != 0 || short.Hours != nil {
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
