package snapshotgraph

import (
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

func makeSnap(objs []heapsnapshot.Object) *heapsnapshot.HeapSnapshot {
	return &heapsnapshot.HeapSnapshot{Objects: objs}
}

// mustBuild builds the graph and then computes reachability. Tests that
// only need the structural side of Build call Build directly.
func mustBuild(t *testing.T, snap *heapsnapshot.HeapSnapshot) *Analysis {
	t.Helper()
	a, err := Build(snap, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ComputeReachability(a)
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
	if gr.Roots[0].Kind != "stack" || gr.Roots[0].Detail != "main.run" || gr.Roots[0].Ptr != 0x1000 {
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
	// Interior pointer must NOT resolve to a zero-sized object — it has no
	// range registered, only the exact-address ByAddr entry.
	if _, ok := a.Graph.FindObjectContaining(0x1001); ok {
		t.Fatalf("interior pointer 0x1001 must not resolve to zero-sized object")
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
	if a.Stats.ZeroObjectPointers != 1 {
		t.Fatalf("ZeroObjectPointers = %d, want 1 (per pointer-accounting invariant)", a.Stats.ZeroObjectPointers)
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
	}
	a := mustBuild(t, snap)
	for _, w := range a.Warnings {
		if strings.HasPrefix(w, "parse: truncated object") {
			return
		}
	}
	t.Fatalf("expected parser warning to propagate; got %v", a.Warnings)
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

// nil snapshot is a hard error, not a panic.
func TestBuildNilSnapshotError(t *testing.T) {
	if _, err := Build(nil, Options{}); err == nil {
		t.Fatalf("expected error on nil snapshot")
	}
}

// An empty snapshot must produce a non-nil zero-valued Analysis with
// non-nil reachability maps after ComputeReachability (callers should
// not have to nil-check).
func TestBuildEmptySnapshot(t *testing.T) {
	a := mustBuild(t, &heapsnapshot.HeapSnapshot{})
	if a.Graph == nil {
		t.Fatalf("Graph must not be nil")
	}
	if len(a.Graph.Objects) != 0 || len(a.Goroutines) != 0 {
		t.Fatalf("expected empty graph, got %d objects / %d goroutines",
			len(a.Graph.Objects), len(a.Goroutines))
	}
	if a.Globals.Reachable == nil {
		t.Fatalf("GlobalReachability.Reachable must not be nil even when no roots")
	}
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d", a.Stats.UnreachableObjects)
	}
}

// Build alone must produce roots and structural stats but leave
// reachability sets and reach-derived stats untouched. Reachability is
// the job of ComputeReachability; the /debug/memusage path skips it.
func TestBuildIsStructuralOnly(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0x2000}},
			{Addr: 0x2000, Size: 8},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}},
		}},
		Globals: []heapsnapshot.Root{{Kind: "data", PointerAddr: 0x2000}},
	}
	a, err := Build(snap, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Structural side is filled.
	if got := len(a.Graph.Objects); got != 2 {
		t.Fatalf("len(Objects) = %d, want 2", got)
	}
	if got := a.Stats.Edges; got != 1 {
		t.Fatalf("Stats.Edges = %d, want 1", got)
	}
	if got := len(a.Goroutines[0].Roots); got != 1 {
		t.Fatalf("len(Roots) = %d, want 1", got)
	}
	if got := len(a.Globals.Roots); got != 1 {
		t.Fatalf("len(Globals.Roots) = %d, want 1", got)
	}
	// Reachability side is NOT filled by Build.
	if a.Goroutines[0].Reachable != nil {
		t.Fatalf("Goroutines[0].Reachable should be nil before ComputeReachability, got %v", a.Goroutines[0].Reachable)
	}
	if a.Globals.Reachable != nil {
		t.Fatalf("Globals.Reachable should be nil before ComputeReachability, got %v", a.Globals.Reachable)
	}
	if a.Stats.GoroutineReachableObjects != 0 {
		t.Fatalf("GoroutineReachableObjects = %d, want 0 before ComputeReachability", a.Stats.GoroutineReachableObjects)
	}
	if a.Stats.GlobalReachableObjects != 0 {
		t.Fatalf("GlobalReachableObjects = %d, want 0 before ComputeReachability", a.Stats.GlobalReachableObjects)
	}
	if a.Stats.SharedByGoroutinesObjects != 0 {
		t.Fatalf("SharedByGoroutinesObjects = %d, want 0 before ComputeReachability", a.Stats.SharedByGoroutinesObjects)
	}
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d, want 0 before ComputeReachability", a.Stats.UnreachableObjects)
	}

	// After ComputeReachability the sets and counters are populated.
	ComputeReachability(a)
	if a.Goroutines[0].Reachable == nil || len(a.Goroutines[0].Reachable) != 2 {
		t.Fatalf("Goroutines[0].Reachable size = %d, want 2", len(a.Goroutines[0].Reachable))
	}
	if a.Globals.Reachable == nil || len(a.Globals.Reachable) != 1 {
		t.Fatalf("Globals.Reachable size = %d, want 1", len(a.Globals.Reachable))
	}
	if a.Stats.GoroutineReachableObjects != 2 {
		t.Fatalf("GoroutineReachableObjects = %d, want 2", a.Stats.GoroutineReachableObjects)
	}
	if a.Stats.GlobalReachableObjects != 1 {
		t.Fatalf("GlobalReachableObjects = %d, want 1", a.Stats.GlobalReachableObjects)
	}
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d, want 0", a.Stats.UnreachableObjects)
	}

	// Idempotent.
	prevReach := a.Goroutines[0].Reachable
	ComputeReachability(a)
	if len(a.Goroutines[0].Reachable) != len(prevReach) {
		t.Fatalf("ComputeReachability not idempotent: size %d -> %d", len(prevReach), len(a.Goroutines[0].Reachable))
	}
}

// Reachability through a chain of interior pointers: A points into B's
// interior, B points into C's interior. DFS must follow both hops.
func TestTransitiveInteriorReachability(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 64, PointerAddrs: []uint64{0x2010}}, // -> B.interior
			{Addr: 0x2000, Size: 64, PointerAddrs: []uint64{0x3020}}, // -> C.interior
			{Addr: 0x3000, Size: 64},
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:     1,
			Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1008}}}, // -> A.interior
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

// SharedByGoroutinesObjects counts objects reachable from more than one
// goroutine — independently of whether the same object is also reachable
// from globals.
func TestSharedCountIgnoresGlobals(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8, PointerAddrs: []uint64{0x3000}},
			{Addr: 0x2000, Size: 8, PointerAddrs: []uint64{0x3000}},
			{Addr: 0x3000, Size: 8}, // shared
		},
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 1, Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x1000}}}},
			{ID: 2, Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x2000}}}},
		},
		Globals: []heapsnapshot.Root{
			{Kind: "data", PointerAddr: 0x3000}, // global also references shared
		},
	}
	a := mustBuild(t, snap)
	sharedID := idFor(t, a.Graph, 0x3000)
	// Sharing is computed across goroutines only; global reachability is
	// reported separately.
	if a.Stats.SharedByGoroutinesObjects != 1 {
		t.Fatalf("SharedByGoroutinesObjects = %d", a.Stats.SharedByGoroutinesObjects)
	}
	if _, ok := a.Globals.Reachable[sharedID]; !ok {
		t.Fatalf("shared object should also be in global reachable set")
	}
	// Whole-process reachable = goroutine ∪ global. Should equal 3 here.
	if a.Stats.UnreachableObjects != 0 {
		t.Fatalf("UnreachableObjects = %d", a.Stats.UnreachableObjects)
	}
}

// snap.Data / snap.BSS pointers produce data/bss global roots — and the
// builder must not double-count them against snap.Globals, which the
// parser also fills from the same segments.
func TestGlobalRootDedupAcrossSources(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 8},
		},
		// The parser publishes the same data-segment pointer in both
		// snap.Globals (Kind="data", Addr=slot) and snap.Data (PointerAddrs +
		// matching Fields). Phase 4 must de-duplicate by (kind, ptr, slot).
		Globals: []heapsnapshot.Root{
			{Kind: "data", Addr: 0xd008, PointerAddr: 0x1000},
		},
		Data: []heapsnapshot.DataSegment{{
			Kind:         "data",
			Addr:         0xd000,
			Size:         16,
			Fields:       []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 8}},
			PointerAddrs: []uint64{0x1000},
		}},
	}
	a := mustBuild(t, snap)
	count := 0
	for _, r := range a.Globals.Roots {
		if r.Kind == "data" && r.Ptr == 0x1000 && r.SlotAddr == 0xd008 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 dedup'd data root, got %d (roots=%+v)", count, a.Globals.Roots)
	}
	if a.Stats.GlobalRoots != 1 {
		t.Fatalf("GlobalRoots = %d", a.Stats.GlobalRoots)
	}
}

// A pointer to an object's exact base address goes through the ByAddr
// fast path, not the range index. This guards against a regression where
// FindObjectContaining could resolve nonsense via overlapping ranges if
// ByAddr was removed.
func TestByAddrFastPathForExactBase(t *testing.T) {
	a := mustBuild(t, makeSnap([]heapsnapshot.Object{
		{Addr: 0x1000, Size: 16},
		{Addr: 0x2000, Size: 16},
	}))
	g := a.Graph
	id, ok := g.FindObjectContaining(0x2000)
	if !ok {
		t.Fatalf("exact base lookup failed")
	}
	if g.Objects[id].Addr != 0x2000 {
		t.Fatalf("got Addr 0x%x, want 0x2000", g.Objects[id].Addr)
	}
	// Sanity: address 0x2000 is also the start of its own range, so the
	// binary-search path must return the same answer if ByAddr were
	// bypassed. Verify by stripping ByAddr and re-running.
	delete(g.ByAddr, 0x2000)
	id2, ok := g.FindObjectContaining(0x2000)
	if !ok || g.Objects[id2].Addr != 0x2000 {
		t.Fatalf("range-index fallback failed: ok=%t id=%d", ok, id2)
	}
}

// A finalizer registered on an object that also references itself should
// not cause double-counting or non-termination.
func TestFinalizerOnSelfReferentialObject(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 16, PointerAddrs: []uint64{0x1000}}, // self
		},
		Finalizers: []heapsnapshot.Finalizer{{ObjectAddr: 0x1000}},
	}
	a := mustBuild(t, snap)
	if len(a.Globals.Roots) != 1 {
		t.Fatalf("global roots = %d", len(a.Globals.Roots))
	}
	if a.Stats.GlobalReachableObjects != 1 {
		t.Fatalf("GlobalReachableObjects = %d", a.Stats.GlobalReachableObjects)
	}
	id := idFor(t, a.Graph, 0x1000)
	if !hasChild(a.Graph, id, id) {
		t.Fatalf("expected self edge")
	}
}

// TestFinalizerTargetNotChargedToUnrelatedGoroutine is the explicit Phase C
// acceptance check for criterion 6: a finalizer-rooted object must not be
// included in a BFS from an unrelated user goroutine's roots.
//
// Model:
//
//	object A — reachable only from a finalizer (global root)
//	object B — reachable only from a user goroutine's stack root
//
// ReachableFrom(goroutine roots) must return {B} only.
// Globals.Reachable must contain {A}.
// Selected BFS via ReachableFrom must not include A.
func TestFinalizerTargetNotChargedToUnrelatedGoroutine(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 64},  // A: finalizer target
			{Addr: 0x2000, Size: 128}, // B: owned by user goroutine
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID: 7,
			Frames: []heapsnapshot.StackFrame{{
				FuncName:     "user.work",
				PointerAddrs: []uint64{0x2000}, // goroutine root -> B only
			}},
		}},
		Finalizers: []heapsnapshot.Finalizer{{ObjectAddr: 0x1000}}, // finalizer -> A
	}

	a := mustBuild(t, snap)

	aID := idFor(t, a.Graph, 0x1000)
	bID := idFor(t, a.Graph, 0x2000)

	// Finalizer root must be in Globals, not in any goroutine.
	if len(a.Globals.Roots) != 1 || a.Globals.Roots[0].Kind != "finalizer" {
		t.Fatalf("expected one finalizer global root, got %+v", a.Globals.Roots)
	}
	if _, ok := a.Globals.Reachable[aID]; !ok {
		t.Fatalf("A must be globally reachable (via finalizer root)")
	}

	// Goroutine BFS must reach B but not A.
	gr := a.Goroutines[0]
	if _, ok := gr.Reachable[bID]; !ok {
		t.Fatalf("B must be reachable from goroutine 7's roots")
	}
	if _, ok := gr.Reachable[aID]; ok {
		t.Fatalf("A must NOT be in goroutine 7's reachable set (it is finalizer-rooted only)")
	}

	// Direct ReachableFrom — the code path used by /debug/memusage — must also
	// return only B when called with the goroutine's roots.
	selected := ReachableFrom(a.Graph, gr.Roots)
	if _, ok := selected[bID]; !ok {
		t.Fatalf("ReachableFrom(goroutine roots): B missing")
	}
	if _, ok := selected[aID]; ok {
		t.Fatalf("ReachableFrom(goroutine roots): A must not appear (finalizer object not charged to unrelated goroutine)")
	}
	if len(selected) != 1 {
		t.Fatalf("ReachableFrom(goroutine roots) size = %d, want 1", len(selected))
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

// Scheduler-held roots: gp.sched.ctxt, gp._defer, and gp._panic from the
// goroutine record must root this goroutine's reach (the GC scans them as
// goroutine-owned roots in runtime.scanstack). The defer record's own
// pointer fields then pull in its closure through ordinary graph edges.
func TestSchedulerRoots(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Size: 16},                                 // sched.ctxt closure
			{Addr: 0x2000, Size: 32, PointerAddrs: []uint64{0x3000}}, // heap _defer record
			{Addr: 0x3000, Size: 16},                                 // deferred fn closure
			{Addr: 0x4000, Size: 16},                                 // heap _panic record
		},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:      9,
			Context: 0x1000,
			Defer:   0x2000,
			Panic:   0x4000,
		}},
	}
	a := mustBuild(t, snap)
	gr := a.Goroutines[0]

	wantKinds := map[string]uint64{
		"sched.ctxt": 0x1000,
		"defer":      0x2000,
		"panic":      0x4000,
	}
	if len(gr.Roots) != len(wantKinds) {
		t.Fatalf("roots = %+v, want %d scheduler roots", gr.Roots, len(wantKinds))
	}
	for _, r := range gr.Roots {
		want, ok := wantKinds[r.Kind]
		if !ok {
			t.Fatalf("unexpected root kind %q (root %+v)", r.Kind, r)
		}
		if r.Ptr != want {
			t.Fatalf("root kind %q ptr = 0x%x, want 0x%x", r.Kind, r.Ptr, want)
		}
		delete(wantKinds, r.Kind)
	}

	for _, addr := range []uint64{0x1000, 0x2000, 0x3000, 0x4000} {
		id := idFor(t, a.Graph, addr)
		if _, ok := gr.Reachable[id]; !ok {
			t.Fatalf("object 0x%x missing from goroutine reach", addr)
		}
	}
	if a.Stats.UnresolvedGoroutineRoots != 0 {
		t.Fatalf("UnresolvedGoroutineRoots = %d, want 0", a.Stats.UnresolvedGoroutineRoots)
	}
}

// Unresolved scheduler pointers are the normal case (_panic records and
// deferprocStack defers live on the stack, not the heap): they must be
// skipped silently, producing neither roots nor unresolved-root counts.
func TestSchedulerRootsUnresolvedSkippedSilently(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Size: 16}},
		Goroutines: []heapsnapshot.Goroutine{{
			ID:      3,
			Context: 0xdead0000, // stack address: not a heap object
			Defer:   0xbeef0000,
			Panic:   0xc0de0000,
		}},
	}
	a := mustBuild(t, snap)
	gr := a.Goroutines[0]
	if len(gr.Roots) != 0 {
		t.Fatalf("roots = %+v, want none", gr.Roots)
	}
	if a.Stats.UnresolvedGoroutineRoots != 0 {
		t.Fatalf("UnresolvedGoroutineRoots = %d, want 0 (sched pointers skip silently)", a.Stats.UnresolvedGoroutineRoots)
	}
	if len(a.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", a.Warnings)
	}
}
