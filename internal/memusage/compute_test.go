package memusage

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestObjectSetBytesAndIntersect(t *testing.T) {
	g := newTestGraph(t, []testObject{
		{addr: 0x1000, size: 10},
		{addr: 0x2000, size: 20},
		{addr: 0x3000, size: 30},
	})
	a := setOf(0, 1)
	b := setOf(1, 2)

	if bytes := ObjectSetBytes(g, a); bytes != 30 {
		t.Fatalf("ObjectSetBytes(a) = %d, want 30", bytes)
	}
	count, bytes := IntersectCountBytes(g, a, b)
	if count != 1 || bytes != 20 {
		t.Fatalf("intersection of a∩b = (%d, %d), want (1, 20)", count, bytes)
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
		Roots:       rootsForIDs(0, 1), // A, B
	}
	user2 := snapshotgraph.GoroutineReachability{
		GoroutineID: 12,
		Roots:       rootsForIDs(1), // B
	}
	other := snapshotgraph.GoroutineReachability{
		GoroutineID: 13,
		Roots:       rootsForIDs(2), // C
	}
	sys := snapshotgraph.GoroutineReachability{
		GoroutineID: 14,
		IsSystem:    true,
		Roots:       rootsForIDs(1, 3), // B, D — system overlap on B
	}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user1, user2, other, sys},
		Globals:    snapshotgraph.GlobalReachability{Roots: rootsForIDs(0)}, // A is also a global
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
	user := snapshotgraph.GoroutineReachability{GoroutineID: 1, Roots: rootsForIDs(0)}
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

func TestComputeFromAnalysis_StringMissingIsErrorEvenWithMatches(t *testing.T) {
	// goroutine 1 decoded and matches the selector; goroutine 2 had a
	// string_missing decode failure. The non-authoritative match count must
	// NOT produce a 200 — return StringMissingError instead.
	g := newTestGraph(t, []testObject{
		{addr: 0x1000, size: 10},
		{addr: 0x2000, size: 20},
	})
	user1 := snapshotgraph.GoroutineReachability{GoroutineID: 1, Roots: rootsForIDs(0)}
	user2 := snapshotgraph.GoroutineReachability{GoroutineID: 2, Roots: rootsForIDs(1)}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user1, user2},
	}
	labelsByGID := map[uint64]map[string]string{
		1: {"job": "42"},
	}
	diag := Diagnostics{
		StringMissingCount: 1,
		FailedGoroutines:   1,
		GoVersion:          "go1.26.3",
		GOARCH:             "amd64",
		Warnings:           []string{"some labels not readable"},
	}
	_, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "42"}},
		analysis,
		labelsByGID,
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
	if sme.GoVersion != "go1.26.3" || sme.GOARCH != "amd64" {
		t.Fatalf("StringMissingError = %+v", sme)
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

// rootsForIDs builds a slice of stack RootRefs pointing at the given
// object IDs. Used for tests that need to drive the on-demand traversal
// without constructing a parser-shaped HeapSnapshot.
func rootsForIDs(ids ...snapshotgraph.ObjectID) []snapshotgraph.RootRef {
	out := make([]snapshotgraph.RootRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, snapshotgraph.RootRef{ObjectID: id, Kind: "stack"})
	}
	return out
}

func TestComputeFromAnalysis_ValidatesRequest(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	analysis := &snapshotgraph.Analysis{
		Graph: g,
		Goroutines: []snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Roots: rootsForIDs(0)},
		},
	}
	// Empty want must NOT match every goroutine.
	_, err := ComputeFromAnalysis(Request{Labels: map[string]string{}}, analysis, nil, Diagnostics{}, Options{})
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if verr.Code != "empty_labels" {
		t.Fatalf("code = %q, want empty_labels", verr.Code)
	}
}

func TestComputeFromAnalysis_NoMatchWithStringMissing(t *testing.T) {
	// Unrelated goroutines did decode labels successfully (labelsByGID
	// non-empty), but the requested selector matches nothing AND the
	// decoder reported string_missing for at least one goroutine. The
	// honest answer is 422 string_missing, not 200 "no match".
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	user1 := snapshotgraph.GoroutineReachability{GoroutineID: 1, Roots: rootsForIDs(0)}
	user2 := snapshotgraph.GoroutineReachability{GoroutineID: 2, Roots: rootsForIDs(0)}
	analysis := &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: []snapshotgraph.GoroutineReachability{user1, user2},
	}
	labelsByGID := map[uint64]map[string]string{
		1: {"job": "other"},
	}
	diag := Diagnostics{StringMissingCount: 1, FailedGoroutines: 1}
	_, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"job": "alpha"}},
		analysis,
		labelsByGID,
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

func TestReachableFromGoroutines_NilGraph(t *testing.T) {
	result := reachableFromGoroutines(nil, []*snapshotgraph.GoroutineReachability{
		{GoroutineID: 1},
	})
	if len(result) != 0 {
		t.Fatalf("expected empty set for nil graph, got %d items", len(result))
	}
}

func TestReachableFromGoroutines_ZeroRoots(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	goroutines := []*snapshotgraph.GoroutineReachability{
		{GoroutineID: 1, Roots: nil}, // non-empty slice but no roots
	}
	result := reachableFromGoroutines(g, goroutines)
	if len(result) != 0 {
		t.Fatalf("expected empty set for zero-root goroutines, got %d items", len(result))
	}
}

func TestReachableFromGoroutines_EmptyGoroutines(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 10}})
	result := reachableFromGoroutines(g, nil)
	if len(result) != 0 {
		t.Fatalf("expected empty set for empty goroutines, got %d items", len(result))
	}
}

func TestObjectSetBytes_NilGraph(t *testing.T) {
	set := map[snapshotgraph.ObjectID]struct{}{0: {}}
	if got := ObjectSetBytes(nil, set); got != 0 {
		t.Fatalf("ObjectSetBytes(nil, …) = %d, want 0", got)
	}
}

func TestComputeFromAnalysis_NilAnalysis(t *testing.T) {
	_, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"a": "b"}},
		nil,
		nil,
		Diagnostics{},
		Options{},
	)
	if err == nil {
		t.Fatal("expected error for nil analysis, got nil")
	}
}

func TestComputeFromAnalysis_NilGraph(t *testing.T) {
	analysis := &snapshotgraph.Analysis{Graph: nil}
	_, err := ComputeFromAnalysis(
		Request{Labels: map[string]string{"a": "b"}},
		analysis,
		nil,
		Diagnostics{},
		Options{},
	)
	if err == nil {
		t.Fatal("expected error for nil graph, got nil")
	}
}

func TestDiagnosticsFromHeapLabels_FailedGoroutinesWarning(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{BuildVersion: "go1.26.3", GOARCH: "amd64"},
	}
	res := heaplabels.Result{
		Stats: heaplabels.Stats{
			GoroutinesTotal:  2,
			GoroutinesFailed: 2,
			// StringsMissing < FailedGoroutines triggers the "failed" warning.
			StringsMissing: 0,
		},
	}
	diag := DiagnosticsFromHeapLabels(snap, res)
	if diag.FailedGoroutines != 2 {
		t.Fatalf("FailedGoroutines = %d, want 2", diag.FailedGoroutines)
	}
	found := false
	for _, w := range diag.Warnings {
		if strings.Contains(w, "failed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected failed-goroutines warning, got warnings=%v", diag.Warnings)
	}
}

func TestCopyLabels_NilInput(t *testing.T) {
	if copyLabels(nil) != nil {
		t.Fatal("copyLabels(nil) should return nil")
	}
}

func TestComputeFromAnalysis_SuccessOmitsDebugFields(t *testing.T) {
	// Encode a successful Response to JSON and verify that debug fields
	// removed from the struct are not present. This guards against
	// re-introducing them via json tags or embedded structs.
	resp := &Response{
		Labels:            map[string]string{"job": "42"},
		MatchedGoroutines: 1,
		ReachableObjects:  1,
		ReachableBytes:    10,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"attribution", "go_version", "goarch", "warnings", "label_source"} {
		if _, present := raw[forbidden]; present {
			t.Errorf("success response must not contain %q, but it does: %s", forbidden, data)
		}
	}
}

func TestComputeFromAnalysis_ZeroOverlapFieldsPresent(t *testing.T) {
	// Overlap fields must be included in JSON even when zero (no omitempty).
	resp := &Response{
		Labels:            map[string]string{"job": "42"},
		MatchedGoroutines: 0,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{
		"global_overlap_objects", "global_overlap_bytes",
		"system_overlap_objects", "system_overlap_bytes",
	} {
		if _, present := raw[field]; !present {
			t.Errorf("overlap field %q must be present even when zero, but it is absent: %s", field, data)
		}
	}
}

func TestObjectSetBytes_HugeObjectID(t *testing.T) {
	g := newTestGraph(t, []testObject{{addr: 0x1000, size: 7}})
	// A huge ObjectID must be skipped, not panic, and not pollute byte
	// counts. Its conversion through uint64 mustn't accidentally look
	// in-range on platforms where int < uint64.
	set := map[snapshotgraph.ObjectID]struct{}{
		0:                          {},
		^snapshotgraph.ObjectID(0): {}, // max ObjectID
	}
	if got := ObjectSetBytes(g, set); got != 7 {
		t.Fatalf("ObjectSetBytes = %d, want 7 (huge ID must be ignored)", got)
	}
}
