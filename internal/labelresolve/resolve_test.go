package labelresolve

import (
	"encoding/binary"
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

func TestHeapLabelsWinOverManifestAndProfile(t *testing.T) {
	snap := snapWithHeapLabels(1, map[string]string{"bubble": "heap"})
	m := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "manifest"}},
		},
	}
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "profile", "goid": "1"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}

	res := ResolveLabels(snap, m, prof, Options{})
	if !reflect.DeepEqual(res.LabelsByGID[1], map[string]string{"bubble": "heap"}) {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.SourcesByGID[1] != SourceHeap {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
	if res.MatchedFromHeap != 1 || res.MatchedFromManifest != 0 || res.MatchedFromProfile != 0 {
		t.Fatalf("matches heap=%d manifest=%d profile=%d", res.MatchedFromHeap, res.MatchedFromManifest, res.MatchedFromProfile)
	}
}

func TestUnsupportedHeapLayoutFallsBackToManifest(t *testing.T) {
	snap := snapWithUnsupportedHeapLayout(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	m := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "manifest"}},
		},
	}

	res := ResolveLabels(snap, m, nil, Options{})
	if res.SourcesByGID[1] != SourceManifest {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
	if res.LabelsByGID[1]["bubble"] != "manifest" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.MatchedFromHeap != 0 || res.MatchedFromManifest != 1 {
		t.Fatalf("matches heap=%d manifest=%d", res.MatchedFromHeap, res.MatchedFromManifest)
	}
	if !hasWarningContaining(res.Warnings, "heap label recovery unsupported") {
		t.Fatalf("missing unsupported heap warning: %v", res.Warnings)
	}
}

func TestUnsupportedHeapLayoutFallsBackToProfile(t *testing.T) {
	snap := snapWithUnsupportedHeapLayout(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	prof := &goroutineprofile.Profile{
		Goroutines: []goroutineprofile.GoroutineSample{
			{
				Labels: map[string]string{"bubble": "profile", "goid": "1"},
				Count:  1,
				Frames: []goroutineprofile.Frame{{Func: "worker.loop"}},
			},
		},
	}

	res := ResolveLabels(snap, nil, prof, Options{})
	if res.SourcesByGID[1] != SourceProfileID {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
	if res.LabelsByGID[1]["bubble"] != "profile" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.MatchedFromHeap != 0 || res.MatchedFromProfile != 1 {
		t.Fatalf("matches heap=%d profile=%d", res.MatchedFromHeap, res.MatchedFromProfile)
	}
	if !hasWarningContaining(res.Warnings, "heap label recovery unsupported") {
		t.Fatalf("missing unsupported heap warning: %v", res.Warnings)
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

func snapWithHeapLabels(gid uint64, labels map[string]string) *heapsnapshot.HeapSnapshot {
	gLabelsOffset := uint64(0x160)
	gObj := make([]byte, 0x180)
	putPtr(gObj, int(gLabelsOffset), 0x1000)

	labelMap := make([]byte, 24)
	putPtr(labelMap, 0, 0x2000)
	putPtr(labelMap, 8, uint64(len(labels)))
	putPtr(labelMap, 16, uint64(len(labels)))

	labelArray := make([]byte, len(labels)*32)
	objects := []heapsnapshot.Object{
		{Addr: 0x5000, Contents: gObj},
		{Addr: 0x1000, Contents: labelMap},
		{Addr: 0x2000, Contents: labelArray},
	}

	nextString := uint64(0x3000)
	i := 0
	for k, v := range labels {
		keyAddr := nextString
		nextString += 0x100
		valueAddr := nextString
		nextString += 0x100
		putStringHeader(labelArray, i*32, keyAddr, k)
		putStringHeader(labelArray, i*32+16, valueAddr, v)
		objects = append(objects,
			heapsnapshot.Object{Addr: keyAddr, Contents: []byte(k)},
			heapsnapshot.Object{Addr: valueAddr, Contents: []byte(v)},
		)
		i++
	}

	return &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			PtrSize:      8,
			GOARCH:       "amd64",
			BuildVersion: "go1.26.3-X:nodwarf5",
		},
		Objects: objects,
		Goroutines: []heapsnapshot.Goroutine{
			{ID: gid, Addr: 0x5000, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
		},
	}
}

func putStringHeader(buf []byte, off int, addr uint64, s string) {
	putPtr(buf, off, addr)
	putPtr(buf, off+8, uint64(len(s)))
}

func putPtr(buf []byte, off int, value uint64) {
	binary.LittleEndian.PutUint64(buf[off:off+8], value)
}

func snapWithUnsupportedHeapLayout(goroutines ...heapsnapshot.Goroutine) *heapsnapshot.HeapSnapshot {
	return &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			PtrSize:      8,
			GOARCH:       "amd64",
			BuildVersion: "go9.99-test",
		},
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Contents: []byte{1}},
		},
		Goroutines: goroutines,
	}
}

func hasWarningContaining(warnings []string, substr string) bool {
	for _, w := range warnings {
		if contains(w, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
