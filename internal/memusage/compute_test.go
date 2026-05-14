package memusage

import (
	"errors"
	"reflect"
	"testing"

	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshotgraph"
)

func TestLabelsMatch(t *testing.T) {
	tests := []struct {
		name string
		have map[string]string
		want map[string]string
		out  bool
	}{
		{
			name: "single match",
			have: map[string]string{"job": "42", "tenant": "acme"},
			want: map[string]string{"job": "42"},
			out:  true,
		},
		{
			name: "all match",
			have: map[string]string{"job": "42", "tenant": "acme"},
			want: map[string]string{"job": "42", "tenant": "acme"},
			out:  true,
		},
		{
			name: "missing want key",
			have: map[string]string{"job": "42"},
			want: map[string]string{"job": "42", "tenant": "acme"},
			out:  false,
		},
		{
			name: "value mismatch",
			have: map[string]string{"job": "42"},
			want: map[string]string{"job": "99"},
			out:  false,
		},
		{
			name: "nil have",
			have: nil,
			want: map[string]string{"job": "42"},
			out:  false,
		},
		{
			name: "empty want matches anything",
			have: map[string]string{"job": "42"},
			want: map[string]string{},
			out:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LabelsMatch(tt.have, tt.want); got != tt.out {
				t.Fatalf("LabelsMatch = %v, want %v", got, tt.out)
			}
		})
	}
}

func TestObjectSetBytesAndUnion(t *testing.T) {
	g := newTestGraph(t, []testObject{
		{addr: 0x1000, size: 10},
		{addr: 0x2000, size: 20},
		{addr: 0x3000, size: 30},
	})
	g1 := snapshotgraph.GoroutineReachability{GoroutineID: 1, Reachable: setOf(0, 1)}
	g2 := snapshotgraph.GoroutineReachability{GoroutineID: 2, Reachable: setOf(1, 2)}

	union := UnionReachable([]snapshotgraph.GoroutineReachability{g1, g2})
	if got := len(union); got != 3 {
		t.Fatalf("len(union) = %d, want 3", got)
	}
	if bytes := ObjectSetBytes(g, union); bytes != 60 {
		t.Fatalf("union bytes = %d, want 60 (counted-once semantics)", bytes)
	}

	count, bytes := IntersectCountBytes(g, g1.Reachable, g2.Reachable)
	if count != 1 || bytes != 20 {
		t.Fatalf("intersection of g1∩g2 = (%d, %d), want (1, 20)", count, bytes)
	}
}

func TestComputeFromAnalysis_SystemExclusionAndOverlap(t *testing.T) {
	g := newTestGraph(t, []testObject{
		{addr: 0x1000, size: 10}, // 0: A
		{addr: 0x2000, size: 20}, // 1: B
		{addr: 0x3000, size: 30}, // 2: C
		{addr: 0x4000, size: 40}, // 3: D
	})
	user1 := snapshotgraph.GoroutineReachability{
		GoroutineID: 11,
		Reachable:   setOf(0, 1), // A, B
	}
	user2 := snapshotgraph.GoroutineReachability{
		GoroutineID: 12,
		Reachable:   setOf(1), // B
	}
	other := snapshotgraph.GoroutineReachability{
		GoroutineID: 13,
		Reachable:   setOf(2), // C
	}
	sys := snapshotgraph.GoroutineReachability{
		GoroutineID: 14,
		IsSystem:    true,
		Reachable:   setOf(1, 3), // B, D — system overlap on B
	}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user1, user2, other, sys},
		Globals:    snapshotgraph.GlobalReachability{Reachable: setOf(0)}, // A is also a global
	}
	labelsByGID := map[uint64]map[string]string{
		11: {"job": "42"},
		12: {"job": "42", "tenant": "acme"},
		13: {"job": "99"},
		14: {"job": "42"},
	}
	diag := Diagnostics{GoVersion: "go1.26.3", GOARCH: "amd64"}

	// Default: system excluded.
	resp, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "42"}},
		analysis,
		labelsByGID,
		diag,
		Options{},
	)
	if err != nil {
		t.Fatalf("ComputeFromAnalysis: %v", err)
	}
	if resp.MatchedGoroutines != 2 {
		t.Fatalf("matched = %d, want 2 (user1, user2)", resp.MatchedGoroutines)
	}
	if resp.ReachableObjects != 2 || resp.ReachableBytes != 30 {
		t.Fatalf("reachable = (%d objects, %d bytes), want (2, 30)", resp.ReachableObjects, resp.ReachableBytes)
	}
	if resp.GlobalOverlapObjects != 1 || resp.GlobalOverlapBytes != 10 {
		t.Fatalf("global overlap = (%d, %d), want (1, 10)", resp.GlobalOverlapObjects, resp.GlobalOverlapBytes)
	}
	if resp.SystemOverlapObjects != 1 || resp.SystemOverlapBytes != 20 {
		t.Fatalf("system overlap = (%d, %d), want (1, 20)", resp.SystemOverlapObjects, resp.SystemOverlapBytes)
	}
	if resp.Attribution != AttributionHeapNative {
		t.Fatalf("attribution = %q, want %q", resp.Attribution, AttributionHeapNative)
	}

	// IncludeSystemGoroutines flips the count and adds D into reachable.
	resp, err = ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "42"}},
		analysis,
		labelsByGID,
		diag,
		Options{IncludeSystemGoroutines: true},
	)
	if err != nil {
		t.Fatalf("ComputeFromAnalysis (include system): %v", err)
	}
	if resp.MatchedGoroutines != 3 {
		t.Fatalf("matched (include system) = %d, want 3", resp.MatchedGoroutines)
	}
	if resp.ReachableObjects != 3 || resp.ReachableBytes != 70 {
		t.Fatalf("reachable (include system) = (%d, %d), want (3, 70)", resp.ReachableObjects, resp.ReachableBytes)
	}
	// systemOverlap is reported only when system goroutines are excluded.
	if resp.SystemOverlapObjects != 0 || resp.SystemOverlapBytes != 0 {
		t.Fatalf("system overlap when included = (%d, %d), want zero", resp.SystemOverlapObjects, resp.SystemOverlapBytes)
	}
}

func TestComputeFromAnalysis_UnsupportedRuntime(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	analysis := &snapshotgraph.Analysis{Graph: g}
	diag := Diagnostics{
		UnsupportedRuntime: true,
		GoVersion:          "go1.27.0",
		GOARCH:             "amd64",
	}
	_, err := ComputeFromAnalysis(Request{Labels: map[string]string{"job": "42"}}, analysis, nil, diag, Options{})
	if err == nil {
		t.Fatal("expected UnsupportedRuntimeError, got nil")
	}
	var ure *UnsupportedRuntimeError
	if !errors.As(err, &ure) {
		t.Fatalf("error = %v, want UnsupportedRuntimeError", err)
	}
	if ure.GoVersion != "go1.27.0" || ure.GOARCH != "amd64" {
		t.Fatalf("UnsupportedRuntimeError = %+v", ure)
	}
}

func TestComputeFromAnalysis_StringMissingNoLabels(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	user := snapshotgraph.GoroutineReachability{GoroutineID: 1, Reachable: setOf(0)}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user},
	}
	diag := Diagnostics{
		StringMissingCount: 1,
		FailedGoroutines:   1,
		GoVersion:          "go1.26.3",
		GOARCH:             "amd64",
	}

	// No labels recovered at all -> StringMissingError.
	_, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "42"}},
		analysis,
		map[uint64]map[string]string{}, // no recovered labels
		diag,
		Options{},
	)
	if err == nil {
		t.Fatal("expected StringMissingError, got nil")
	}
	var sme *StringMissingError
	if !errors.As(err, &sme) {
		t.Fatalf("error = %v, want StringMissingError", err)
	}
}

func TestComputeFromAnalysis_IncompleteAttribution(t *testing.T) {
	g := newTestGraph(t, []testObject{
		{addr: 0x1000, size: 10},
		{addr: 0x2000, size: 20},
	})
	user1 := snapshotgraph.GoroutineReachability{GoroutineID: 1, Reachable: setOf(0)}
	user2 := snapshotgraph.GoroutineReachability{GoroutineID: 2, Reachable: setOf(1)}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user1, user2},
	}
	// goroutine 2 had a string_missing error; goroutine 1 decoded.
	labelsByGID := map[uint64]map[string]string{
		1: {"job": "42"},
	}
	diag := Diagnostics{
		StringMissingCount: 1,
		FailedGoroutines:   1,
		Warnings:           []string{"some labels not readable"},
	}
	resp, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "42"}},
		analysis,
		labelsByGID,
		diag,
		Options{},
	)
	if err != nil {
		t.Fatalf("ComputeFromAnalysis: %v", err)
	}
	if resp.MatchedGoroutines != 1 || resp.ReachableObjects != 1 || resp.ReachableBytes != 10 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Attribution != AttributionHeapNativeIncomplete {
		t.Fatalf("attribution = %q, want %q", resp.Attribution, AttributionHeapNativeIncomplete)
	}
}

func TestDiagnosticsFromHeapLabels(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			BuildVersion: "go1.26.3",
			GOARCH:       "amd64",
		},
	}

	// All unsupported.
	res := heaplabels.Result{
		Stats: heaplabels.Stats{
			GoroutinesTotal:       3,
			GoroutinesUnsupported: 3,
		},
	}
	diag := DiagnosticsFromHeapLabels(snap, res)
	if !diag.UnsupportedRuntime {
		t.Fatalf("UnsupportedRuntime = false, want true")
	}
	if diag.GoVersion != "go1.26.3" || diag.GOARCH != "amd64" {
		t.Fatalf("version metadata not forwarded: %+v", diag)
	}

	// String_missing path surfaces warning.
	res = heaplabels.Result{
		Stats: heaplabels.Stats{
			GoroutinesTotal:    2,
			GoroutinesDecoded:  1,
			GoroutinesFailed:   1,
			StringsMissing:     1,
		},
	}
	diag = DiagnosticsFromHeapLabels(snap, res)
	if diag.UnsupportedRuntime {
		t.Fatalf("UnsupportedRuntime = true, want false")
	}
	if diag.StringMissingCount != 1 || diag.FailedGoroutines != 1 {
		t.Fatalf("counters = %+v", diag)
	}
	if len(diag.Warnings) == 0 {
		t.Fatalf("expected at least one warning")
	}
}

// --- helpers ---

type testObject struct {
	addr uint64
	size uint64
}

func newTestGraph(t *testing.T, objs []testObject) *snapshotgraph.Graph {
	t.Helper()
	g := &snapshotgraph.Graph{
		ByAddr: make(map[uint64]snapshotgraph.ObjectID, len(objs)),
	}
	for _, o := range objs {
		id := snapshotgraph.ObjectID(len(g.Objects))
		g.Objects = append(g.Objects, snapshotgraph.Object{
			ID:   id,
			Addr: o.addr,
			Size: o.size,
		})
		g.ByAddr[o.addr] = id
	}
	return g
}

func setOf(ids ...snapshotgraph.ObjectID) map[snapshotgraph.ObjectID]struct{} {
	out := make(map[snapshotgraph.ObjectID]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// Reflect-based sanity: makes sure Reachable values are real maps, not
// nil shadows, when ComputeFromAnalysis iterates them.
func TestUnionReachable_NilSafe(t *testing.T) {
	gr := snapshotgraph.GoroutineReachability{Reachable: nil}
	got := UnionReachable([]snapshotgraph.GoroutineReachability{gr})
	want := map[snapshotgraph.ObjectID]struct{}{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnionReachable(nil reachable) = %v, want %v", got, want)
	}
}
