package bubblereport

import (
	"bytes"
	"strings"
	"testing"

	"bubblepprof/internal/snapshotgraph"
)

func TestReportPrintSummaryNil(t *testing.T) {
	var buf bytes.Buffer
	var r *Report
	r.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "<nil>") {
		t.Fatalf("expected <nil>, got %q", buf.String())
	}
}

func TestReportPrintSummaryNoBubbles(t *testing.T) {
	r := &Report{
		Diagnostics: Diagnostics{
			HeapGoroutines:          5,
			UserGoroutines:          3,
			SystemGoroutines:        2,
			LabeledUserGoroutines:   0,
			UnlabeledUserGoroutines: 3,
			IgnoredSystemGoroutines: 2,
		},
	}
	var buf bytes.Buffer
	r.PrintSummary(&buf)
	got := buf.String()
	for _, want := range []string{
		"heap goroutines: 5",
		"user goroutines: 3 (labeled: 0, unlabeled: 3)",
		"system goroutines: 2 (ignored: 2)",
		"no bubbles found",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestReportPrintSummaryWithBubbles(t *testing.T) {
	r := &Report{
		Diagnostics: Diagnostics{
			HeapGoroutines:        2,
			UserGoroutines:        2,
			LabeledUserGoroutines: 2,
		},
		Groups: []LabelGroup{
			{
				Key: "bubble",
				Bubbles: []Bubble{
					{
						Key: "bubble", Value: "alpha",
						GoroutineIDs:     []uint64{1, 2},
						ReachableObjects: map[snapshotgraph.ObjectID]struct{}{1: {}, 2: {}, 3: {}},
						ReachableBytes:   300,
						ExclusiveObjects: map[snapshotgraph.ObjectID]struct{}{1: {}},
						ExclusiveBytes:   100,
						SharedObjects:    map[snapshotgraph.ObjectID]struct{}{2: {}, 3: {}},
						SharedBytes:      200,
						GlobalOverlapObjects: 1,
						GlobalOverlapBytes:   50,
						SystemOverlapObjects: 2,
						SystemOverlapBytes:   75,
					},
					{
						Key: "bubble", Value: "beta",
						GoroutineIDs:     []uint64{3},
						ReachableObjects: map[snapshotgraph.ObjectID]struct{}{4: {}},
						ReachableBytes:   400,
					},
				},
				SharedObjects: map[snapshotgraph.ObjectID]struct{}{2: {}, 3: {}},
				SharedBytes:   200,
			},
		},
	}
	var buf bytes.Buffer
	r.PrintSummary(&buf)
	got := buf.String()
	for _, want := range []string{
		"label group: bubble",
		"bubble=alpha",
		"bubble=beta",
		"reachable bytes: 300",
		"exclusive bytes: 100",
		"shared bytes: 200",
		"global overlap objects: 1",
		"global overlap bytes: 50",
		"system overlap objects: 2",
		"system overlap bytes: 75",
		"group shared objects: 2",
		"group shared bytes: 200",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
