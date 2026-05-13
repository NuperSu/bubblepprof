package snapshotgraph

import (
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func makeSnap(objs []heapsnapshot.Object) *heapsnapshot.HeapSnapshot {
	return &heapsnapshot.HeapSnapshot{Objects: objs}
}

func mustBuild(t *testing.T, snap *heapsnapshot.HeapSnapshot) *Analysis {
	t.Helper()
	a, err := Build(snap, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return a
}

func idFor(t *testing.T, g *Graph, addr uint64) ObjectID {
	t.Helper()
	id, ok := g.ByAddr[addr]
	if !ok {
		t.Fatalf("no object at 0x%x", addr)
	}
	return id
}

func hasChild(g *Graph, from, to ObjectID) bool {
	for _, c := range g.Objects[from].Children {
		if c == to {
			return true
		}
	}
	return false
}

func childCount(g *Graph, from, to ObjectID) int {
	n := 0
	for _, c := range g.Objects[from].Children {
		if c == to {
			n++
		}
	}
	return n
}

// Test 1: exact address edge.
func TestExactAddressEdge(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 10, PointerAddrs: []uint64{0x2000}},
		{Addr: 0x2000, Size: 10},
	})
	a := mustBuild(t, snap)
	g := a.Graph
	aID := idFor(t, g, 0x1000)
	bID := idFor(t, g, 0x2000)
	if !hasChild(g, aID, bID) {
		t.Fatalf("expected A -> B edge; A.Children=%v", g.Objects[aID].Children)
	}
	if len(g.Objects[bID].Children) != 0 {
		t.Fatalf("expected B to have no children; got %v", g.Objects[bID].Children)
	}
}

// Test 2: interior pointer edge.
func TestInteriorPointerEdge(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 100, PointerAddrs: []uint64{0x2040}},
		{Addr: 0x2000, Size: 100},
	})
	a := mustBuild(t, snap)
	g := a.Graph
	aID := idFor(t, g, 0x1000)
	bID := idFor(t, g, 0x2000)
	if !hasChild(g, aID, bID) {
		t.Fatalf("interior pointer 0x2040 should resolve to B; A.Children=%v", g.Objects[aID].Children)
	}
	if a.Stats.UnresolvedPointers != 0 {
		t.Fatalf("UnresolvedPointers = %d", a.Stats.UnresolvedPointers)
	}
}

// Test 3: unresolved pointer.
func TestUnresolvedPointer(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 10, PointerAddrs: []uint64{0x9999}},
	})
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	if len(a.Graph.Objects[aID].Children) != 0 {
		t.Fatalf("expected no children")
	}
	if a.Stats.UnresolvedPointers != 1 {
		t.Fatalf("UnresolvedPointers = %d", a.Stats.UnresolvedPointers)
	}
	if a.Stats.RawObjectPointers != 1 {
		t.Fatalf("RawObjectPointers = %d", a.Stats.RawObjectPointers)
	}
}

// Test 4: goroutine roots, reachability through one hop.
func TestGoroutineRoots(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 10, PointerAddrs: []uint64{0x2000}},
			{Addr: 0x2000, Size: 10},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID: 7,
			Frames: []heapsnapshot.StackFrame{{
				FuncName:     "main.run",
				PointerAddrs: []uint64{0x1000},
			}},
		}},
	}
	a := mustBuild(t, snap)
	if len(a.Goroutines) != 1 {
		t.Fatalf("Goroutines len = %d", len(a.Goroutines))
	}
	gr := a.Goroutines[0]
	if gr.GoroutineID != 7 {
		t.Fatalf("goroutine id = %d", gr.GoroutineID)
	}
	if len(gr.Roots) != 1 {
		t.Fatalf("expected 1 stack root, got %d", len(gr.Roots))
	}
	if gr.Roots[0].Kind != "stack" || gr.Roots[0].Detail != "main.run" {
		t.Fatalf("root = %+v", gr.Roots[0])
	}
	aID := idFor(t, a.Graph, 0x1000)
	bID := idFor(t, a.Graph, 0x2000)
	if _, ok := gr.Reachable[aID]; !ok {
		t.Fatalf("A missing from reachable")
	}
	if _, ok := gr.Reachable[bID]; !ok {
		t.Fatalf("B missing from reachable")
	}
	if len(gr.Reachable) != 2 {
		t.Fatalf("reachable size = %d", len(gr.Reachable))
	}
}

// Test 5: stack interior pointer resolves to containing object.
func TestStackInteriorPointerRoot(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 100},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1040}}},
		}},
	}
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	gr := a.Goroutines[0]
	if len(gr.Reachable) != 1 {
		t.Fatalf("reachable size = %d", len(gr.Reachable))
	}
	if _, ok := gr.Reachable[aID]; !ok {
		t.Fatalf("A missing from reachable")
	}
}

// Test 6: global roots are not merged into goroutine reachability.
func TestGlobalRootsSeparate(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 10},
			{Addr: 0x2000, Size: 10},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     5,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}},
		}},
		Globals: []heapsnapshot.Root{{
			Kind:        "data",
			PointerAddr: 0x2000,
		}},
	}
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	bID := idFor(t, a.Graph, 0x2000)

	gr := a.Goroutines[0]
	if _, ok := gr.Reachable[aID]; !ok {
		t.Fatalf("A missing from goroutine reachable")
	}
	if _, ok := gr.Reachable[bID]; ok {
		t.Fatalf("B must NOT be in goroutine reachable (it's global-only)")
	}
	if _, ok := a.Globals.Reachable[bID]; !ok {
		t.Fatalf("B missing from global reachable")
	}
	if _, ok := a.Globals.Reachable[aID]; ok {
		t.Fatalf("A must NOT be in global reachable (it's stack-only)")
	}
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d", a.Stats.UnreachableObjects)
	}
}

// Test 7: cycles terminate.
func TestCyclesTerminate(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 10, PointerAddrs: []uint64{0x2000}},
			{Addr: 0x2000, Size: 10, PointerAddrs: []uint64{0x3000}},
			{Addr: 0x3000, Size: 10, PointerAddrs: []uint64{0x1000}},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}},
		}},
	}
	a := mustBuild(t, snap)
	gr := a.Goroutines[0]
	if len(gr.Reachable) != 3 {
		t.Fatalf("reachable size = %d", len(gr.Reachable))
	}
	for _, addr := range []uint64{0x1000, 0x2000, 0x3000} {
		if _, ok := gr.Reachable[idFor(t, a.Graph, addr)]; !ok {
			t.Fatalf("0x%x missing from reachable", addr)
		}
	}
}

// Self-edge: pointer to itself.
func TestSelfEdgeTerminates(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 16, PointerAddrs: []uint64{0x1008}},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}},
		}},
	}
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	if !hasChild(a.Graph, aID, aID) {
		t.Fatalf("expected A -> A self edge; got %v", a.Graph.Objects[aID].Children)
	}
	gr := a.Goroutines[0]
	if len(gr.Reachable) != 1 {
		t.Fatalf("reachable size = %d", len(gr.Reachable))
	}
}

// Test 8: shared object between two goroutines.
func TestSharedObject(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0x3000}}, // A -> Shared
			{Addr: 0x2000, Size: 8, PointerAddrs: []uint64{0x3000}}, // B -> Shared
			{Addr: 0x3000, Size: 8},                                 // Shared
		},
		Goroutines: []heapsnapshot.Goroutine{
			{
				ID:     1,
				Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}},
			},
			{
				ID:     2,
				Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x2000}}},
			},
		},
	}
	a := mustBuild(t, snap)
	if a.Stats.SharedByGoroutinesObjects != 1 {
		t.Fatalf("SharedByGoroutinesObjects = %d", a.Stats.SharedByGoroutinesObjects)
	}
	sharedID := idFor(t, a.Graph, 0x3000)
	for _, gr := range a.Goroutines {
		if _, ok := gr.Reachable[sharedID]; !ok {
			t.Fatalf("goroutine %d missing shared object", gr.GoroutineID)
		}
	}
	if a.Stats.GoroutineReachableObjects != 3 {
		t.Fatalf("GoroutineReachableObjects = %d", a.Stats.GoroutineReachableObjects)
	}
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d", a.Stats.UnreachableObjects)
	}
}

// Test 9: duplicate / interior pointers to the same object are deduped.
func TestDuplicateEdgesAreDeduped(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0x2000, 0x2000, 0x2040}},
		{Addr: 0x2000, Size: 100},
	})
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	bID := idFor(t, a.Graph, 0x2000)
	if got := childCount(a.Graph, aID, bID); got != 1 {
		t.Fatalf("A -> B count = %d", got)
	}
	if a.Stats.RawObjectPointers != 3 {
		t.Fatalf("RawObjectPointers = %d", a.Stats.RawObjectPointers)
	}
	if a.Stats.ResolvedObjectPointers != 3 {
		t.Fatalf("ResolvedObjectPointers = %d", a.Stats.ResolvedObjectPointers)
	}
	if a.Stats.Edges != 1 {
		t.Fatalf("Edges = %d", a.Stats.Edges)
	}
}

// Test 10: overlapping ranges produce a warning, do not panic.
func TestOverlappingRangesWarn(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 100},
		{Addr: 0x1050, Size: 100},
	})
	a := mustBuild(t, snap)
	if len(a.Warnings) == 0 {
		t.Fatalf("expected overlap warning")
	}
	foundOverlap := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "overlapping") {
			foundOverlap = true
		}
	}
	if !foundOverlap {
		t.Fatalf("warnings: %v", a.Warnings)
	}
	// FindObjectContaining must remain deterministic at the overlap.
	if _, ok := a.Graph.FindObjectContaining(0x1080); !ok {
		t.Fatalf("expected lookup at 0x1080 to succeed")
	}
}

// Pointer to object end is exclusive: addr+size does not belong to the
// object.
func TestPointerToObjectEndExclusive(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 100, PointerAddrs: []uint64{0x2064}}, // 0x2000+0x64 = 0x2064 (end)
		{Addr: 0x2000, Size: 100},
	})
	a := mustBuild(t, snap)
	if a.Stats.UnresolvedPointers != 1 {
		t.Fatalf("UnresolvedPointers = %d", a.Stats.UnresolvedPointers)
	}
	if a.Stats.ResolvedObjectPointers != 0 {
		t.Fatalf("ResolvedObjectPointers = %d", a.Stats.ResolvedObjectPointers)
	}
}

// Zero-sized object: included in Graph.Objects, not in range index. A
// pointer to its base address still resolves via ByAddr; an interior
// pointer does not.
func TestZeroSizedObject(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 0},
		{Addr: 0x2000, Size: 8, PointerAddrs: []uint64{0x1000}},
	})
	a := mustBuild(t, snap)
	if _, ok := a.Graph.ByAddr[0x1000]; !ok {
		t.Fatalf("zero-sized object should be in Graph.ByAddr")
	}
	if a.Stats.UnresolvedPointers != 0 {
		t.Fatalf("UnresolvedPointers = %d", a.Stats.UnresolvedPointers)
	}
	bID := idFor(t, a.Graph, 0x2000)
	zID := idFor(t, a.Graph, 0x1000)
	if !hasChild(a.Graph, bID, zID) {
		t.Fatalf("expected B -> zero-sized edge")
	}
	foundWarn := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "zero-sized") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("expected zero-sized warning, got %v", a.Warnings)
	}
}

// Duplicate object addresses: keep every parsed node, but keep the first
// address mapping deterministic and warn.
func TestDuplicateAddressWarn(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
		{Addr: 0x1000, Size: 16}, // dup
	})
	a := mustBuild(t, snap)
	if len(a.Graph.Objects) != 2 {
		t.Fatalf("expected both object nodes kept, got %d", len(a.Graph.Objects))
	}
	if a.Graph.Objects[0].Size != 8 {
		t.Fatalf("kept second object instead of first; size = %d", a.Graph.Objects[0].Size)
	}
	if a.Graph.ByAddr[0x1000] != 0 {
		t.Fatalf("ByAddr should keep first object for duplicate address, got ID %d", a.Graph.ByAddr[0x1000])
	}
	foundWarn := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "duplicate") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("expected duplicate warning, got %v", a.Warnings)
	}
}

// Nil pointer in object pointer list does not produce edge.
func TestNilPointerIgnored(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0}},
	})
	a := mustBuild(t, snap)
	aID := idFor(t, a.Graph, 0x1000)
	if len(a.Graph.Objects[aID].Children) != 0 {
		t.Fatalf("nil pointer must not produce edge")
	}
	if a.Stats.RawObjectPointers != 1 {
		t.Fatalf("RawObjectPointers = %d", a.Stats.RawObjectPointers)
	}
	if a.Stats.UnresolvedPointers != 0 {
		t.Fatalf("zero pointer must not count as unresolved")
	}
}

// FindObjectContaining direct unit checks.
func TestFindObjectContaining(t *testing.T) {
	a := mustBuild(t, makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 100},
		{Addr: 0x2000, Size: 50},
	}))
	g := a.Graph

	cases := []struct {
		ptr    uint64
		wantOK bool
		want   uint64
	}{
		{0x0, false, 0},
		{0x999, false, 0},
		{0x1000, true, 0x1000},
		{0x1063, true, 0x1000},
		{0x1064, false, 0}, // exclusive end
		{0x1500, false, 0},
		{0x2000, true, 0x2000},
		{0x2031, true, 0x2000},
		{0x2032, false, 0},
		{0xffff, false, 0},
	}
	for _, c := range cases {
		id, ok := g.FindObjectContaining(c.ptr)
		if ok != c.wantOK {
			t.Errorf("FindObjectContaining(0x%x) ok = %t, want %t", c.ptr, ok, c.wantOK)
			continue
		}
		if ok && g.Objects[id].Addr != c.want {
			t.Errorf("FindObjectContaining(0x%x) -> 0x%x, want 0x%x", c.ptr, g.Objects[id].Addr, c.want)
		}
	}
}

// Finalizer roots show up as global roots.
func TestFinalizerRoot(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8},
			{Addr: 0x2000, Size: 8},
		},
		Finalizers:       []heapsnapshot.Finalizer{{ObjectAddr: 0x1000}},
		QueuedFinalizers: []heapsnapshot.QueuedFinalizer{{ObjectAddr: 0x2000}},
	}
	a := mustBuild(t, snap)
	if len(a.Globals.Roots) != 2 {
		t.Fatalf("global roots = %d", len(a.Globals.Roots))
	}
	kinds := map[string]int{}
	for _, r := range a.Globals.Roots {
		kinds[r.Kind]++
	}
	if kinds["finalizer"] != 1 || kinds["queued_finalizer"] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
	if a.Stats.GlobalReachableObjects != 2 {
		t.Fatalf("GlobalReachableObjects = %d", a.Stats.GlobalReachableObjects)
	}
}

// Unreachable objects are counted but not treated as errors.
func TestUnreachableCounted(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
		{Addr: 0x2000, Size: 8},
	})
	a := mustBuild(t, snap)
	if a.Stats.UnreachableObjects != 2 {
		t.Fatalf("UnreachableObjects = %d", a.Stats.UnreachableObjects)
	}
}

// Strict mode promotes overlap to an error.
func TestStrictModeOverlapError(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 100},
		{Addr: 0x1050, Size: 100},
	})
	if _, err := Build(snap, Options{Strict: true}); err == nil {
		t.Fatalf("expected error in strict mode")
	}
}

// Parser warnings are propagated into Analysis.Warnings with a "parse:"
// prefix so snapshot graph callers see them too.
func TestParserWarningsPropagate(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Warnings: []string{"truncated object 0x42"},
		Stats: heapsnapshot.ParseStats{
			InterfaceFieldsSkipped: 3,
			EfaceFieldsSkipped:     2,
		},
	}
	a := mustBuild(t, snap)
	foundParse := false
	foundIface := false
	for _, w := range a.Warnings {
		if strings.HasPrefix(w, "parse: truncated object") {
			foundParse = true
		}
		if strings.Contains(w, "3 interface and 2 eface") {
			foundIface = true
		}
	}
	if !foundParse {
		t.Fatalf("expected parser warning to propagate; got %v", a.Warnings)
	}
	if !foundIface {
		t.Fatalf("expected iface/eface skipped warning; got %v", a.Warnings)
	}
}

// Address 0 is never resolvable, even if a zero-addr object exists.
func TestAddressZeroNotResolvable(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0, Size: 16},
		{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0}},
	})
	a := mustBuild(t, snap)
	if _, ok := a.Graph.FindObjectContaining(0); ok {
		t.Fatalf("FindObjectContaining(0) must not resolve")
	}
	if _, ok := a.Graph.ByAddr[0]; ok {
		t.Fatalf("address 0 must not appear in ByAddr")
	}
	foundWarn := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "address 0") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("expected zero-address warning; got %v", a.Warnings)
	}
}

// Unresolved pointers are broken down by source category.
func TestUnresolvedBreakdown(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0xdead}}, // object ptr
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0xbeef}}},
		}},
		Globals:          []heapsnapshot.Root{{PointerAddr: 0xcafe}},
		Finalizers:       []heapsnapshot.Finalizer{{ObjectAddr: 0xf00d}},
		QueuedFinalizers: []heapsnapshot.QueuedFinalizer{{ObjectAddr: 0xfade}},
	}
	a := mustBuild(t, snap)
	if a.Stats.UnresolvedObjectPointers != 1 {
		t.Fatalf("UnresolvedObjectPointers = %d", a.Stats.UnresolvedObjectPointers)
	}
	if a.Stats.UnresolvedGoroutineRoots != 1 {
		t.Fatalf("UnresolvedGoroutineRoots = %d", a.Stats.UnresolvedGoroutineRoots)
	}
	if a.Stats.UnresolvedGlobalRoots != 1 {
		t.Fatalf("UnresolvedGlobalRoots = %d", a.Stats.UnresolvedGlobalRoots)
	}
	if a.Stats.UnresolvedFinalizerRoots != 2 {
		t.Fatalf("UnresolvedFinalizerRoots = %d", a.Stats.UnresolvedFinalizerRoots)
	}
	if a.Stats.UnresolvedPointers != 5 {
		t.Fatalf("UnresolvedPointers = %d (want 5)", a.Stats.UnresolvedPointers)
	}
}

// AddEdge tolerates invalid IDs and returns whether it actually appended.
func TestAddEdgeReturnAndValidate(t *testing.T) {
	a := mustBuild(t, makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
		{Addr: 0x2000, Size: 8},
	}))
	g := a.Graph
	if !g.AddEdge(0, 1) {
		t.Fatalf("first AddEdge should return true")
	}
	if g.AddEdge(0, 1) {
		t.Fatalf("duplicate AddEdge should return false")
	}
	if g.AddEdge(99, 1) {
		t.Fatalf("invalid from-ID should return false (and not panic)")
	}
	if g.AddEdge(0, 99) {
		t.Fatalf("invalid to-ID should return false (and not panic)")
	}
}

// System goroutines (g0, GC workers, finalizer goroutine, …) must be
// surfaced through GoroutineReachability.IsSystem so later phases can
// filter them out of bubble attribution. The runtime emits IsSystem in
// each goroutine record; Phase 4 just has to forward it.
func TestSystemGoroutineFlagPropagated(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8},
			{Addr: 0x2000, Size: 8},
		},
		Goroutines: []heapsnapshot.Goroutine{
			{
				ID:       1,
				IsSystem: false,
				Frames: []heapsnapshot.StackFrame{{
					PointerAddrs: []uint64{0x1000},
				}},
			},
			{
				ID:           99,
				IsSystem:     true,
				IsBackground: true,
				Frames: []heapsnapshot.StackFrame{{
					PointerAddrs: []uint64{0x2000},
				}},
			},
		},
	}
	a := mustBuild(t, snap)
	if len(a.Goroutines) != 2 {
		t.Fatalf("Goroutines len = %d", len(a.Goroutines))
	}
	for _, gr := range a.Goroutines {
		switch gr.GoroutineID {
		case 1:
			if gr.IsSystem || gr.IsBackground {
				t.Fatalf("goroutine 1 misflagged: %+v", gr)
			}
		case 99:
			if !gr.IsSystem || !gr.IsBackground {
				t.Fatalf("goroutine 99 should be IsSystem and IsBackground: %+v", gr)
			}
		default:
			t.Fatalf("unexpected goroutine id %d", gr.GoroutineID)
		}
	}
	if a.Stats.SystemGoroutines != 1 {
		t.Fatalf("SystemGoroutines = %d, want 1", a.Stats.SystemGoroutines)
	}
}

// Stack-root SlotAddr must be filled in from the parser-supplied
// PointerSlots so later phases can attribute roots to specific stack
// locations.
func TestStackRootSlotAddrPreserved(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID: 1,
			Frames: []heapsnapshot.StackFrame{{
				SP:           0xc000_0000,
				PointerAddrs: []uint64{0x1000},
				PointerSlots: []uint64{0xc000_0010}, // sp+0x10
			}},
		}},
	}
	a := mustBuild(t, snap)
	gr := a.Goroutines[0]
	if len(gr.Roots) != 1 {
		t.Fatalf("roots = %d", len(gr.Roots))
	}
	if gr.Roots[0].SlotAddr != 0xc000_0010 {
		t.Fatalf("SlotAddr = 0x%x, want 0xc000_0010", gr.Roots[0].SlotAddr)
	}
}

// If a future parser stops emitting PointerSlots for some frames, the
// builder must not panic and SlotAddr defaults to zero rather than
// mis-attributing to a wrong slot.
func TestStackRootMissingSlotsTolerated(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID: 1,
			Frames: []heapsnapshot.StackFrame{{
				PointerAddrs: []uint64{0x1000},
				// PointerSlots intentionally nil.
			}},
		}},
	}
	a := mustBuild(t, snap)
	gr := a.Goroutines[0]
	if len(gr.Roots) != 1 {
		t.Fatalf("roots = %d", len(gr.Roots))
	}
	if gr.Roots[0].SlotAddr != 0 {
		t.Fatalf("SlotAddr should default to 0, got 0x%x", gr.Roots[0].SlotAddr)
	}
}

// Pointer-accounting invariant:
//
//	RawObjectPointers = ZeroObjectPointers + ResolvedObjectPointers + UnresolvedObjectPointers
//
// Holds whether or not the input contains zero pointers — including the
// synthetic case where a test passes a literal nil pointer through.
func TestPointerAccountingInvariant(t *testing.T) {
	snap := makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 16, PointerAddrs: []uint64{
			0x2000, // resolved
			0,      // zero
			0xdead, // unresolved
			0x2008, // resolved (interior)
		}},
		{Addr: 0x2000, Size: 16},
	})
	a := mustBuild(t, snap)
	s := a.Stats
	if s.RawObjectPointers != 4 {
		t.Fatalf("RawObjectPointers = %d", s.RawObjectPointers)
	}
	if s.ZeroObjectPointers != 1 {
		t.Fatalf("ZeroObjectPointers = %d", s.ZeroObjectPointers)
	}
	if s.ResolvedObjectPointers != 2 {
		t.Fatalf("ResolvedObjectPointers = %d", s.ResolvedObjectPointers)
	}
	if s.UnresolvedObjectPointers != 1 {
		t.Fatalf("UnresolvedObjectPointers = %d", s.UnresolvedObjectPointers)
	}
	if s.ZeroObjectPointers+s.ResolvedObjectPointers+s.UnresolvedObjectPointers != s.RawObjectPointers {
		t.Fatalf("invariant violated: %d + %d + %d != %d",
			s.ZeroObjectPointers, s.ResolvedObjectPointers,
			s.UnresolvedObjectPointers, s.RawObjectPointers)
	}
}

// ReachableFrom must not panic on invalid root or invalid child edges.
func TestReachableFromInvalidIDsSafe(t *testing.T) {
	a := mustBuild(t, makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 8},
	}))
	g := a.Graph
	// Inject an invalid child edge directly.
	g.Objects[0].Children = []ObjectID{99}

	got := ReachableFrom(g, []RootRef{
		{ObjectID: 99}, // invalid root, should be skipped
		{ObjectID: 0},  // valid
	})
	if _, ok := got[0]; !ok {
		t.Fatalf("expected ID 0 in reachable")
	}
	if _, ok := got[99]; ok {
		t.Fatalf("invalid ID 99 should not be in reachable")
	}
	if len(got) != 1 {
		t.Fatalf("reachable size = %d", len(got))
	}
}
