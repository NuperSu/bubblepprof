package labelresolve

import (
	"reflect"
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heapsnapshot"
)

func snapWith(goroutines ...heapsnapshot.Goroutine) *heapsnapshot.HeapSnapshot {
	return &heapsnapshot.HeapSnapshot{Goroutines: goroutines}
}

func gframe(name string) heapsnapshot.StackFrame {
	return heapsnapshot.StackFrame{FuncName: name}
}

func TestManifestExactMatch(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 7, Frames: []heapsnapshot.StackFrame{gframe("main.main")}},
		heapsnapshot.Goroutine{ID: 9, Frames: []heapsnapshot.StackFrame{gframe("main.other")}},
	)
	m := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 7, Labels: map[string]string{"bubble": "alpha"}},
			{ID: 42, Labels: map[string]string{"bubble": "ghost"}},
		},
	}
	res := ResolveLabels(snap, m, nil, Options{})
	if got := res.LabelsByGID[7]["bubble"]; got != "alpha" {
		t.Fatalf("goroutine 7 = %v", res.LabelsByGID[7])
	}
	if res.MatchedFromManifest != 1 {
		t.Fatalf("matched from manifest = %d", res.MatchedFromManifest)
	}
	if res.UnmatchedHeap != 1 {
		t.Fatalf("unmatched heap = %d", res.UnmatchedHeap)
	}
	foundGhostWarn := false
	for _, w := range res.Warnings {
		if w == "manifest goroutine 42 not present in heap dump" {
			foundGhostWarn = true
		}
	}
	if !foundGhostWarn {
		t.Fatalf("missing ghost warning, got %v", res.Warnings)
	}
}

func TestProfileStackSingleMatch(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.run"), gframe("main.main")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.run"}, {Func: "main.main"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if got := res.LabelsByGID[1]["bubble"]; got != "alpha" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.SourcesByGID[1] != SourceProfileStack {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
	if res.MatchedFromProfile != 1 {
		t.Fatalf("matched profile = %d", res.MatchedFromProfile)
	}
}

func TestProfileStackNToN(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
		heapsnapshot.Goroutine{ID: 2, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  2,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if res.LabelsByGID[1]["bubble"] != "alpha" || res.LabelsByGID[2]["bubble"] != "alpha" {
		t.Fatalf("labels = %v", res.LabelsByGID)
	}
	if res.MatchedFromProfile != 2 {
		t.Fatalf("matched profile = %d", res.MatchedFromProfile)
	}
}

func TestProfileStackAmbiguousDistinctLabels(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
		heapsnapshot.Goroutine{ID: 2, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
			{
				Labels: map[string]string{"bubble": "beta"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("expected no labels, got %v", res.LabelsByGID)
	}
	if len(res.AmbiguousGIDs) != 2 || res.AmbiguousMatches != 2 {
		t.Fatalf("ambiguous = %d %v", res.AmbiguousMatches, res.AmbiguousGIDs)
	}
}

func TestProfileStackCountMismatch(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
		heapsnapshot.Goroutine{ID: 2, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("expected no labels, got %v", res.LabelsByGID)
	}
	if res.AmbiguousMatches != 2 {
		t.Fatalf("ambiguous = %d", res.AmbiguousMatches)
	}
}

func TestProfileSampleNoHeapMatch(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("main.main")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "elsewhere.fn"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("expected no labels, got %v", res.LabelsByGID)
	}
	if res.UnmatchedProfile != 1 {
		t.Fatalf("UnmatchedProfile = %d", res.UnmatchedProfile)
	}
	if res.UnmatchedHeap != 1 {
		t.Fatalf("UnmatchedHeap = %d", res.UnmatchedHeap)
	}
}

func TestManifestWinsOverProfile(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	m := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "exact"}},
		},
	}
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "guessed"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, m, prof, Options{})
	if !reflect.DeepEqual(res.LabelsByGID[1], map[string]string{"bubble": "exact"}) {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.SourcesByGID[1] != SourceManifest {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
}

func TestProfileGoidLabel(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 5, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha", "goid": "5"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{})
	if res.SourcesByGID[5] != SourceProfileID {
		t.Fatalf("source = %v", res.SourcesByGID[5])
	}
	if res.LabelsByGID[5]["bubble"] != "alpha" {
		t.Fatalf("labels[5] = %v", res.LabelsByGID[5])
	}
	if res.MatchedFromProfile != 1 {
		t.Fatalf("MatchedFromProfile = %d", res.MatchedFromProfile)
	}
}

func TestDisableProfileSkipsCorrelation(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "alpha"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}
	res := ResolveLabels(snap, nil, prof, Options{DisableProfile: true})
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("DisableProfile should yield no labels: %v", res.LabelsByGID)
	}
	if res.UnmatchedHeap != 1 {
		t.Fatalf("UnmatchedHeap = %d", res.UnmatchedHeap)
	}
}
