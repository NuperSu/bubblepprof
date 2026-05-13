package snapshotgraph

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestDuplicateAddressKeepsEveryParsedObjectNode(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
		{Addr: 0x1000, Size: 16, PointerAddrs: []uint64{0x2000}},
		{Addr: 0x2000, Size: 8},
	})

	a := mustBuild(t, snap)
	if got, want := len(a.Graph.Objects), len(snap.Objects); got != want {
		t.Fatalf("graph objects = %d, want one node per parsed object (%d)", got, want)
	}
	if got := a.Stats.ObjectBytes; got != 32 {
		t.Fatalf("ObjectBytes = %d, want duplicate object bytes included", got)
	}
	if got := a.Graph.ByAddr[0x1000]; got != 0 {
		t.Fatalf("ByAddr for duplicate address should keep first object, got ID %d", got)
	}
	dup := ObjectID(1)
	target := idFor(t, a.Graph, 0x2000)
	if !hasChild(a.Graph, dup, target) {
		t.Fatalf("duplicate-address object should still have outgoing edges; children=%v", a.Graph.Objects[dup].Children)
	}
	if !warningContains(a.Warnings, "duplicate object address") {
		t.Fatalf("expected duplicate warning, got %v", a.Warnings)
	}
}

func TestStrictModeDuplicateAddressError(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
		{Addr: 0x1000, Size: 16},
	})
	if _, err := Build(snap, Options{Strict: true}); err == nil {
		t.Fatal("expected strict duplicate-address error")
	}
}

func TestDataAndBSSSegmentsBecomeGlobalRootsWithoutGlobalsExpansion(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 16},
			{Addr: 0x2000, Size: 16},
		},
		Data: []heapsnapshot.DataSegment{{
			Kind:         "data",
			Addr:         0xd000,
			Fields:       []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 8}},
			PointerAddrs: []uint64{0x1008},
		}},
		BSS: []heapsnapshot.DataSegment{{
			Kind:         "bss",
			Addr:         0xe000,
			Fields:       []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}},
			PointerAddrs: []uint64{0x2000},
		}},
	}

	a := mustBuild(t, snap)
	if got := a.Stats.GlobalRoots; got != 2 {
		t.Fatalf("GlobalRoots = %d, want roots from Data and BSS segments", got)
	}
	kinds := map[string]int{}
	for _, root := range a.Globals.Roots {
		kinds[root.Kind]++
	}
	if kinds["data"] != 1 || kinds["bss"] != 1 {
		t.Fatalf("root kinds = %v, want one data and one bss root", kinds)
	}
	if got := a.Stats.GlobalReachableObjects; got != 2 {
		t.Fatalf("GlobalReachableObjects = %d, want both segment roots reachable", got)
	}
	if got := a.Stats.UnreachableObjects; got != 0 {
		t.Fatalf("UnreachableObjects = %d, want data/bss roots included in whole-process reachability", got)
	}
}

func TestDataAndBSSSegmentsDoNotDuplicateParserExpandedGlobals(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Size: 16}},
		Globals: []heapsnapshot.Root{{
			Kind:        "data",
			Addr:        0xd008,
			PointerAddr: 0x1000,
		}},
		Data: []heapsnapshot.DataSegment{{
			Kind:         "data",
			Addr:         0xd000,
			Fields:       []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 8}},
			PointerAddrs: []uint64{0x1000},
		}},
	}

	a := mustBuild(t, snap)
	if got := a.Stats.GlobalRoots; got != 1 {
		t.Fatalf("GlobalRoots = %d, want parser-expanded global root not duplicated", got)
	}
	if got := len(a.Globals.Roots); got != 1 {
		t.Fatalf("len(Global.Roots) = %d", got)
	}
}

func TestOverlappingRangesWarnAgainstEarlierLongRange(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 0x300},
		{Addr: 0x1100, Size: 0x10},
		{Addr: 0x1200, Size: 0x20},
	})

	a := mustBuild(t, snap)
	overlapWarnings := 0
	for _, w := range a.Warnings {
		if strings.Contains(w, "overlapping object ranges") {
			overlapWarnings++
		}
	}
	if overlapWarnings != 2 {
		t.Fatalf("overlap warnings = %d, want both short ranges reported against earlier long range; warnings=%v",
			overlapWarnings, a.Warnings)
	}
}

func TestOverflowRangeClampsAndResolvesInteriorPointer(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: math.MaxUint64 - 8, Size: 32},
		{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{math.MaxUint64 - 4}},
	})

	a := mustBuild(t, snap)
	source := idFor(t, a.Graph, 0x1000)
	target, ok := a.Graph.FindObjectContaining(math.MaxUint64 - 4)
	if !ok {
		t.Fatal("overflow-clamped range should resolve interior pointer before MaxUint64")
	}
	if !hasChild(a.Graph, source, target) {
		t.Fatalf("expected edge into overflow-clamped object; children=%v", a.Graph.Objects[source].Children)
	}
	if !warningContains(a.Warnings, "overflows uint64") {
		t.Fatalf("expected overflow warning, got %v", a.Warnings)
	}
}

func TestBuildNilSnapshotReturnsError(t *testing.T) {
	if _, err := Build(nil, Options{}); err == nil {
		t.Fatal("expected nil snapshot error")
	}
}

func TestReachableFromNilGraphReturnsEmptySet(t *testing.T) {
	got := ReachableFrom(nil, []RootRef{{ObjectID: 1}})
	if len(got) != 0 {
		t.Fatalf("reachable = %v, want empty set for nil graph", got)
	}
}

func TestPrintSummaryNilAnalysis(t *testing.T) {
	var a *Analysis
	var out bytes.Buffer
	a.PrintSummary(&out)
	if got := out.String(); !strings.Contains(got, "snapshot analysis: <nil>") {
		t.Fatalf("unexpected nil summary:\n%s", got)
	}
}

func TestSummaryIncludesNewCounters(t *testing.T) {
	a := mustBuild(t, &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0}}},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:       1,
			IsSystem: true,
		}},
	})

	var out bytes.Buffer
	a.PrintSummary(&out)
	got := out.String()
	for _, want := range []string{
		"zero object pointers: 1",
		"resolved object edges: 0",
		"goroutines: 1 (system: 1)",
		"goroutine roots: 0",
		"bubble attribution: not implemented in this phase",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

func warningContains(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}
