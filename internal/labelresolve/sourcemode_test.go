package labelresolve

import (
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heapsnapshot"
)

// TestAutoHeapWinsOverManifestAndProfile asserts that in auto mode the
// heap-native source is primary even when manifest and profile sources
// are present.
func TestAutoHeapWinsOverManifestAndProfile(t *testing.T) {
	snap := snapWithHeapLabels(1, map[string]string{"bubble": "heap"})
	manifest := &bubblelabels.Manifest{
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

	res := ResolveLabels(snap, manifest, prof, Options{AllowProfileFallback: true})
	if got := res.LabelsByGID[1]["bubble"]; got != "heap" {
		t.Fatalf("labels[1] = %v, want bubble=heap", res.LabelsByGID[1])
	}
	if res.SourcesByGID[1] != SourceHeap {
		t.Fatalf("source = %v, want SourceHeap", res.SourcesByGID[1])
	}
	if res.Diagnostics.Attribution != AttributionHeapNative {
		t.Fatalf("attribution = %v, want heap-native exact", res.Diagnostics.Attribution)
	}
}

// TestAutoManifestFillsHeapGaps asserts that manifest fills in goroutines
// the heap-native source could not resolve (e.g. unsupported runtime).
func TestAutoManifestFillsHeapGaps(t *testing.T) {
	snap := snapWithUnsupportedHeapLayout(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
		heapsnapshot.Goroutine{ID: 2, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	manifest := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "alpha"}},
		},
	}

	res := ResolveLabels(snap, manifest, nil, Options{})
	if res.SourcesByGID[1] != SourceManifest {
		t.Fatalf("source[1] = %v", res.SourcesByGID[1])
	}
	if res.LabelsByGID[1]["bubble"] != "alpha" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if !res.Diagnostics.UnsupportedHeapLayout {
		t.Fatalf("expected UnsupportedHeapLayout diagnostic")
	}
	if res.Diagnostics.Attribution != AttributionManifest {
		t.Fatalf("attribution = %v, want manifest_exact", res.Diagnostics.Attribution)
	}
	if res.UnmatchedHeap != 1 {
		t.Fatalf("UnmatchedHeap = %d, want 1", res.UnmatchedHeap)
	}
}

// TestAutoProfileNotUsedByDefault asserts that profile labels are NOT
// silently applied in auto mode when AllowProfileFallback is not set.
func TestAutoProfileNotUsedByDefault(t *testing.T) {
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
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("expected no labels in default auto mode, got %v", res.LabelsByGID)
	}
	if res.MatchedFromProfile != 0 {
		t.Fatalf("MatchedFromProfile = %d, want 0", res.MatchedFromProfile)
	}
	if res.Diagnostics.ProfileFallbackUsed {
		t.Fatalf("ProfileFallbackUsed should be false")
	}
	if !hasWarningContaining(res.Warnings, "profile fallback is disabled") {
		t.Fatalf("expected disabled-profile warning, got %v", res.Warnings)
	}
	if res.Diagnostics.Attribution != AttributionNone {
		t.Fatalf("attribution = %v, want none", res.Diagnostics.Attribution)
	}
}

// TestAutoProfileOptInUsesProfile mirrors TestAutoProfileNotUsedByDefault
// but with AllowProfileFallback=true: profile labels must now be applied
// and the resolution must be flagged as best-effort.
func TestAutoProfileOptInUsesProfile(t *testing.T) {
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

	res := ResolveLabels(snap, nil, prof, Options{AllowProfileFallback: true})
	if res.LabelsByGID[1]["bubble"] != "profile" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.SourcesByGID[1] != SourceProfileID {
		t.Fatalf("source[1] = %v", res.SourcesByGID[1])
	}
	if !res.Diagnostics.ProfileFallbackUsed {
		t.Fatalf("ProfileFallbackUsed must be true")
	}
	if res.Diagnostics.Attribution != AttributionBestEffortProfile {
		t.Fatalf("attribution = %v, want best-effort profile", res.Diagnostics.Attribution)
	}
}

// TestHeapOnlyDoesNotFallBackToManifest asserts that SourceModeHeap
// refuses to assign labels from any other source even when heap-native
// recovery fails.
func TestHeapOnlyDoesNotFallBackToManifest(t *testing.T) {
	snap := snapWithUnsupportedHeapLayout(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	manifest := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "manifest"}},
		},
	}

	res := ResolveLabels(snap, manifest, nil, Options{
		Source:            SourceModeHeap,
		RequireHeapLabels: true,
	})
	if len(res.LabelsByGID) != 0 {
		t.Fatalf("heap-only mode should yield no labels: %v", res.LabelsByGID)
	}
	if !res.Diagnostics.UnsupportedHeapLayout {
		t.Fatalf("expected UnsupportedHeapLayout diagnostic")
	}
	if !hasWarningContaining(res.Warnings, "heap-native labels required but unavailable") {
		t.Fatalf("expected required-but-unavailable warning, got %v", res.Warnings)
	}
}

// TestManifestOnlyIgnoresHeap asserts that SourceModeManifest skips
// heap-native recovery even when it would succeed.
func TestManifestOnlyIgnoresHeap(t *testing.T) {
	snap := snapWithHeapLabels(1, map[string]string{"bubble": "heap"})
	manifest := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 1, Labels: map[string]string{"bubble": "manifest"}},
		},
	}

	res := ResolveLabels(snap, manifest, nil, Options{Source: SourceModeManifest})
	if res.SourcesByGID[1] != SourceManifest {
		t.Fatalf("source[1] = %v", res.SourcesByGID[1])
	}
	if res.LabelsByGID[1]["bubble"] != "manifest" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.MatchedFromHeap != 0 {
		t.Fatalf("MatchedFromHeap = %d, want 0", res.MatchedFromHeap)
	}
}

// TestProfileOnlyMarkedBestEffort asserts that SourceModeProfile uses
// the profile exclusively and reports best-effort attribution.
func TestProfileOnlyMarkedBestEffort(t *testing.T) {
	snap := snapWith(
		heapsnapshot.Goroutine{ID: 1, Frames: []heapsnapshot.StackFrame{gframe("worker.loop")}},
	)
	manifest := &bubblelabels.Manifest{
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

	res := ResolveLabels(snap, manifest, prof, Options{Source: SourceModeProfile})
	if res.SourcesByGID[1] != SourceProfileID {
		t.Fatalf("source = %v", res.SourcesByGID[1])
	}
	if res.LabelsByGID[1]["bubble"] != "profile" {
		t.Fatalf("labels[1] = %v", res.LabelsByGID[1])
	}
	if res.MatchedFromManifest != 0 {
		t.Fatalf("MatchedFromManifest = %d, want 0", res.MatchedFromManifest)
	}
	if res.Diagnostics.Attribution != AttributionBestEffortProfile {
		t.Fatalf("attribution = %v, want best-effort profile", res.Diagnostics.Attribution)
	}
}

// TestMixedExactAttribution asserts that when heap-native fills some
// goroutines and labels.json fills others, the attribution is reported
// as mixed exact.
func TestMixedExactAttribution(t *testing.T) {
	// snapWithHeapLabels carries one goroutine (ID 1) with heap-native
	// labels; add a second goroutine that heap-native can't resolve
	// (no g object contents).
	snap := snapWithHeapLabels(1, map[string]string{"bubble": "heap"})
	snap.Goroutines = append(snap.Goroutines, heapsnapshot.Goroutine{
		ID:     2,
		Frames: []heapsnapshot.StackFrame{gframe("worker.loop")},
	})

	manifest := &bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 2, Labels: map[string]string{"bubble": "manifest"}},
		},
	}

	res := ResolveLabels(snap, manifest, nil, Options{})
	if res.SourcesByGID[1] != SourceHeap {
		t.Fatalf("source[1] = %v", res.SourcesByGID[1])
	}
	if res.SourcesByGID[2] != SourceManifest {
		t.Fatalf("source[2] = %v", res.SourcesByGID[2])
	}
	if res.Diagnostics.Attribution != AttributionMixedExact {
		t.Fatalf("attribution = %v, want mixed-exact", res.Diagnostics.Attribution)
	}
}

// TestDiagnosticsCountersPopulated checks the Diagnostics struct exposes
// the per-source counters and AttributionMode in a single place.
func TestDiagnosticsCountersPopulated(t *testing.T) {
	snap := snapWithHeapLabels(1, map[string]string{"bubble": "heap"})
	res := ResolveLabels(snap, nil, nil, Options{})
	if res.Diagnostics.HeapNativeMatches != res.MatchedFromHeap {
		t.Fatalf("Diagnostics.HeapNativeMatches mismatch: %d vs %d",
			res.Diagnostics.HeapNativeMatches, res.MatchedFromHeap)
	}
	if res.Diagnostics.Attribution != AttributionHeapNative {
		t.Fatalf("attribution = %v", res.Diagnostics.Attribution)
	}
}

// TestCorrelationLabelKeyHelper asserts the public helper for filtering
// goid-style profile-only label keys.
func TestCorrelationLabelKeyHelper(t *testing.T) {
	cases := map[string]bool{
		"goid":         true,
		"goroutine_id": true,
		"goroutine":    true,
		"bubble":       false,
		"":             false,
	}
	for k, want := range cases {
		if got := IsCorrelationLabelKey(k); got != want {
			t.Errorf("IsCorrelationLabelKey(%q) = %t, want %t", k, got, want)
		}
	}
}
