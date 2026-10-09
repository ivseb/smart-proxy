package history

import (
	"reflect"
	"testing"
	"time"
)

func TestRecorderComputesIntervalsAcrossReplicas(t *testing.T) {
	r := &Recorder{Interval: 10 * time.Second, Retention: 30 * time.Second}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * 10 * time.Second) }

	r.Record(at(0), map[string]Counts{"a": {Total: 100, Routes: map[string]int64{"web": 100}}})
	if len(r.Points()) != 0 {
		t.Fatal("the first sample is only a baseline")
	}
	r.Record(at(1), map[string]Counts{
		"a": {Total: 110, Routes: map[string]int64{"web": 105, "api": 5}},
		"b": {Total: 3, Routes: map[string]int64{"api": 3}}, // a replica that just started publishing
	})
	r.Record(at(2), map[string]Counts{
		"a": {Total: 4, Routes: map[string]int64{"web": 4}}, // restarted
		"b": {Total: 3, Routes: map[string]int64{"api": 3}},
	})

	points := r.Points()
	if len(points) != 2 {
		t.Fatalf("points = %+v", points)
	}
	if points[0].Requests != 13 || !reflect.DeepEqual(points[0].Routes, map[string]int64{"web": 5, "api": 8}) {
		t.Errorf("first interval = %+v", points[0])
	}
	if points[1].Requests != 4 || !reflect.DeepEqual(points[1].Routes, map[string]int64{"web": 4}) {
		t.Errorf("after a restart = %+v", points[1])
	}

	// Only the retention period is kept.
	for i := 3; i <= 6; i++ {
		r.Record(at(i), map[string]Counts{"a": {Total: 4}})
	}
	points = r.Points()
	if len(points) != 3 || !points[0].At.Equal(at(4)) {
		t.Fatalf("retained %d points starting %v", len(points), points[0].At)
	}
}
