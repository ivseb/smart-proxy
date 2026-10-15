package uptime

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

var rome, _ = time.LoadLocation("Europe/Rome")

func TestBaselineCountsTheReplicasItHasWhenUp(t *testing.T) {
	r := NewRecorder()
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	// Asleep (never seen up: 1 replica assumed), then 3 replicas, then asleep again.
	for i, n := range []int32{0, 0, 3, 3, 0, 0} {
		r.Sample(start.Add(time.Duration(i)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: n}})
	}
	s := r.Snapshot(start)[0]
	if s.Replicas[0] != 6 || s.Baseline[0] != 1+1+3+3+3+3 {
		t.Fatalf("replicas %d baseline %d", s.Replicas[0], s.Baseline[0])
	}
	from, to := LastDays(start, 1, time.UTC)
	if rep := Summarize(s, from, to.Add(time.Hour), time.UTC, Options{}); rep.Baseline != 14 || rep.Baseline-rep.Replicas != 8 {
		t.Fatalf("totals %+v", rep.Totals)
	}
}

func TestHoursBeforeTheBaselineAreEstimated(t *testing.T) {
	r := NewRecorder()
	// Recorded by 2.7: no baseline. Hour 0: up 30 of 60 minutes with 2 replicas; hour 1 asleep.
	r.Replace([]Series{{Namespace: "shop", Workload: "web", Start: 500000, Seen: []byte{60, 60}, Up: []byte{30, 0}, Replicas: []uint32{60, 0}, Wakes: []uint16{1, 0}, Sampled: true}})
	s := r.Snapshot(time.Unix(500001*3600, 0))[0]
	if len(s.Baseline) != 2 || s.Baseline[0] != 120 || s.Baseline[1] != 120 {
		t.Fatalf("estimated baseline %v", s.Baseline)
	}
}

func TestYearDayByDayAndMonthByMonth(t *testing.T) {
	r := NewRecorder()
	// 23:30-00:30 UTC on 30 September is 01:30-02:30 on 1 October in Rome: all on 1 October.
	at := time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		r.Sample(at.Add(time.Duration(i)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: 2}})
	}
	y := r.SnapshotYear(at)[0]
	if len(y.Observed) != 1 || dayDate(y.Start).Format("2006-01-02") != "2026-10-01" || y.Observed[0] != 60 || y.Replicas[0] != 120 || y.Baseline[0] != 120 {
		t.Fatalf("year %+v", y)
	}
	rep := SummarizeYear(y, time.Date(2027, 3, 15, 12, 0, 0, 0, rome))
	if len(rep.Months) != 12 || rep.Months[0].Month != "2026-04" || rep.Months[11].Month != "2027-03" || rep.Months[6].Observed != 60 || rep.Observed != 60 {
		t.Fatalf("months %+v", rep.Months)
	}
	if rep.Since == nil || rep.Since.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("since %v", rep.Since)
	}
	// Beyond 12 months, a month no longer shows; beyond a year, the day is dropped.
	if late := SummarizeYear(y, time.Date(2027, 11, 1, 12, 0, 0, 0, rome)); late.Observed != 0 {
		t.Fatalf("13 months later: %+v", late.Totals)
	}
	r.Sample(at.Add(400*24*time.Hour), []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	if y := r.SnapshotYear(at.Add(400 * 24 * time.Hour))[0]; len(y.Observed) != YearDays {
		t.Fatalf("kept %d days", len(y.Observed))
	}
}

func TestYearIsBuiltFromTheHoursWhenMissing(t *testing.T) {
	r := NewRecorder()
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, rome)
	for i := 0; i < 120; i++ {
		r.Sample(start.Add(time.Duration(i)*time.Minute), []State{{Namespace: "shop", Workload: "web", Replicas: 1}})
	}
	hours := r.Snapshot(start)
	fresh := NewRecorder()
	fresh.Replace(hours)
	fresh.ReplaceYear(nil)
	y := fresh.SnapshotYear(start)
	if len(y) != 1 || y[0].Observed[0] != 120 || y[0].Up[0] != 120 || y[0].Baseline[0] != 120 {
		t.Fatalf("built %+v", y)
	}
}

func TestStoreKeepsTheYear(t *testing.T) {
	ctx := context.Background()
	s := &Store{Client: fake.NewClientset(), Namespace: "sp", Name: "sp-uptime"}
	r := NewRecorder()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	r.Sample(now, []State{{Namespace: "shop", Workload: "web", Replicas: 2}})
	if err := s.SaveYear(ctx, r.SnapshotYear(now)); err != nil {
		t.Fatal(err)
	}
	year, err := s.LoadYear(ctx)
	if err != nil || len(year) != 1 || year[0].Replicas[0] != 2 {
		t.Fatalf("loaded %+v %v", year, err)
	}
}
