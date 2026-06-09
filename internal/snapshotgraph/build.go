package snapshotgraph

import (
	"fmt"
	"math"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// Build converts a parsed HeapSnapshot into an Analysis. It resolves
// pointer values to object IDs (supporting interior pointers), builds
// graph edges with deduplication, and extracts goroutine and global
// roots.
//
// Build is structural only: it does not walk the graph. Reachability
// sets (GoroutineReachability.Reachable, GlobalReachability.Reachable)
// and reach-derived Stats (GoroutineReachableObjects,
// GlobalReachableObjects, SharedByGoroutinesObjects, UnreachableObjects)
// are populated by ComputeReachability. Callers that need whole-process
// reachability should call ComputeReachability immediately after Build.
// Callers that only need reachability for a label-selected subset of
// goroutines (the /debug/memusage handler) should skip ComputeReachability
// and traverse from the union of matched goroutine roots instead, paying
// for one BFS rather than one per goroutine plus globals.
//
// Parser-level warnings from snap.Warnings are forwarded to
// Analysis.Warnings with a "parse: " prefix so downstream consumers see
// every recoverable problem in one place.
func Build(snap *heapsnapshot.HeapSnapshot, opts Options) (*Analysis, error) {
	if snap == nil {
		return nil, fmt.Errorf("snapshotgraph: snapshot is nil")
	}

	g := &Graph{
		ByAddr: make(map[uint64]ObjectID, len(snap.Objects)),
	}
	a := &Analysis{Graph: g}

	for _, w := range snap.Warnings {
		a.Warnings = append(a.Warnings, "parse: "+w)
	}

	zeroAddrWarned := false
	zeroSizedWarned := false
	for i := range snap.Objects {
		src := &snap.Objects[i]

		// Address 0 cannot be used for lookup (would alias with nil
		// pointers). Keep the object in the graph for completeness, but
		// do not add it to ByAddr or ranges.
		if src.Addr == 0 {
			if uint64(len(g.Objects)) > math.MaxUint32 {
				return nil, fmt.Errorf("snapshotgraph: object count %d overflows ObjectID (uint32 max)", len(g.Objects))
			}
			id := ObjectID(len(g.Objects))
			g.Objects = append(g.Objects, Object{
				ID:   id,
				Addr: 0,
				Size: src.Size,
			})
			if !zeroAddrWarned {
				a.Warnings = append(a.Warnings, "object with address 0 ignored for direct lookup")
				zeroAddrWarned = true
			}
			continue
		}

		if existing, ok := g.ByAddr[src.Addr]; ok {
			a.Warnings = append(a.Warnings,
				fmt.Sprintf("duplicate object address 0x%x (existing ID %d)",
					src.Addr, existing))
			if opts.Strict {
				return nil, fmt.Errorf("duplicate object address 0x%x", src.Addr)
			}
			if uint64(len(g.Objects)) > math.MaxUint32 {
				return nil, fmt.Errorf("snapshotgraph: object count %d overflows ObjectID (uint32 max)", len(g.Objects))
			}
			id := ObjectID(len(g.Objects))
			g.Objects = append(g.Objects, Object{
				ID:   id,
				Addr: src.Addr,
				Size: src.Size,
			})
			continue
		}

		if uint64(len(g.Objects)) > math.MaxUint32 {
			return nil, fmt.Errorf("snapshotgraph: object count %d overflows ObjectID (uint32 max)", len(g.Objects))
		}
		id := ObjectID(len(g.Objects))
		g.Objects = append(g.Objects, Object{
			ID:   id,
			Addr: src.Addr,
			Size: src.Size,
		})
		g.ByAddr[src.Addr] = id

		if src.Size == 0 {
			if !zeroSizedWarned {
				a.Warnings = append(a.Warnings,
					fmt.Sprintf("zero-sized object at 0x%x; not registered for interior-pointer lookup", src.Addr))
				zeroSizedWarned = true
			}
			continue
		}
		end := src.Addr + src.Size
		if end < src.Addr {
			a.Warnings = append(a.Warnings,
				fmt.Sprintf("object 0x%x size %d overflows uint64; clamping range end",
					src.Addr, src.Size))
			end = math.MaxUint64
			if opts.Strict {
				return nil, fmt.Errorf("object 0x%x size %d overflows uint64", src.Addr, src.Size)
			}
		}
		g.ranges = append(g.ranges, ObjectRange{Start: src.Addr, End: end, ID: id})
	}

	sort.Slice(g.ranges, func(i, j int) bool {
		return g.ranges[i].Start < g.ranges[j].Start
	})
	if len(g.ranges) > 0 {
		maxEndRange := g.ranges[0]
		for i := 1; i < len(g.ranges); i++ {
			cur := g.ranges[i]
			if cur.Start < maxEndRange.End {
				a.Warnings = append(a.Warnings,
					fmt.Sprintf("overlapping object ranges: 0x%x..0x%x and 0x%x..0x%x",
						maxEndRange.Start, maxEndRange.End, cur.Start, cur.End))
				if opts.Strict {
					return nil, fmt.Errorf("overlapping object ranges at 0x%x", cur.Start)
				}
			}
			if cur.End > maxEndRange.End {
				maxEndRange = cur
			}
		}
	}

	// Resolve edges directly from the parsed snapshot's PointerAddrs.
	// g.Objects[i] corresponds 1:1 with snap.Objects[i] (every iteration
	// of the loop above appends exactly one graph object). Reading from
	// snap avoids copying every object's pointer slice into the graph.
	//
	// Duplicate edges are dropped with an epoch-stamped seen array instead
	// of AddEdge's linear Children scan: the scan is O(children) per insert,
	// which is quadratic for an object with many distinct outgoing pointers
	// (a large []*T backing array). seen[target] holds the 1-based index of
	// the last source object that recorded an edge to target — O(1) per
	// edge, one uint32 per object, freed when Build returns. (Degenerate
	// case: at exactly 2^32 objects the final object's epoch wraps to the
	// zero "unseen" stamp and its duplicate edges are kept; reachability is
	// unaffected since BFS dedups by ObjectID.)
	seen := make([]uint32, len(g.Objects))
	for i := range g.Objects {
		fromID := g.Objects[i].ID
		epoch := uint32(i) + 1
		for _, ptr := range snap.Objects[i].PointerAddrs {
			a.Stats.RawObjectPointers++
			if ptr == 0 {
				a.Stats.ZeroObjectPointers++
				continue
			}
			targetID, ok := g.FindObjectContaining(ptr)
			if !ok {
				a.Stats.UnresolvedObjectPointers++
				continue
			}
			a.Stats.ResolvedObjectPointers++
			if seen[targetID] == epoch {
				continue
			}
			seen[targetID] = epoch
			g.Objects[fromID].Children = append(g.Objects[fromID].Children, targetID)
		}
	}

	for i := range g.Objects {
		a.Stats.Edges += len(g.Objects[i].Children)
	}

	a.Goroutines = make([]GoroutineReachability, 0, len(snap.Goroutines))
	for gi := range snap.Goroutines {
		gr := &snap.Goroutines[gi]
		var roots []RootRef
		for fi := range gr.Frames {
			fr := &gr.Frames[fi]
			for pi, ptr := range fr.PointerAddrs {
				if ptr == 0 {
					continue
				}
				targetID, ok := g.FindObjectContaining(ptr)
				if !ok {
					a.Stats.UnresolvedGoroutineRoots++
					continue
				}
				var slot uint64
				if pi < len(fr.PointerSlots) {
					slot = fr.PointerSlots[pi]
				}
				roots = append(roots, RootRef{
					ObjectID: targetID,
					Ptr:      ptr,
					SlotAddr: slot,
					Kind:     "stack",
					Detail:   fr.FuncName,
				})
			}
		}
		// Scheduler-held roots from the goroutine record. The GC scans
		// gp.sched.ctxt and the gp._defer/_panic chains as goroutine-owned
		// roots (runtime.scanstack), so heap closures of created-but-not-yet-
		// scheduled goroutines and heap-allocated defer records belong to
		// this goroutine's reach, not only to the allgs-based global reach.
		// Unresolved values are the normal case (_panic records and
		// deferprocStack defers live on the stack, ctxt is usually nil), so
		// they are skipped without counting toward UnresolvedGoroutineRoots.
		for _, sched := range [...]struct {
			ptr  uint64
			kind string
		}{
			{gr.Context, "sched.ctxt"},
			{gr.Defer, "defer"},
			{gr.Panic, "panic"},
		} {
			if sched.ptr == 0 {
				continue
			}
			targetID, ok := g.FindObjectContaining(sched.ptr)
			if !ok {
				continue
			}
			roots = append(roots, RootRef{
				ObjectID: targetID,
				Ptr:      sched.ptr,
				Kind:     sched.kind,
			})
		}
		a.Goroutines = append(a.Goroutines, GoroutineReachability{
			GoroutineID:  gr.ID,
			IsSystem:     gr.IsSystem,
			IsBackground: gr.IsBackground,
			Roots:        roots,
		})
		if gr.IsSystem {
			a.Stats.SystemGoroutines++
		}
		a.Stats.GoroutineRootPointers += len(roots)
	}
	a.Stats.Goroutines = len(a.Goroutines)

	var globalRoots []RootRef
	type globalRootKey struct {
		kind   string
		ptr    uint64
		slot   uint64
		detail string
	}
	seenGlobalRoots := map[globalRootKey]struct{}{}
	resolveGlobalRoot := func(kind string, ptr, slot uint64, detail string) {
		if ptr == 0 {
			return
		}
		if kind == "" {
			kind = "otherroot"
		}
		key := globalRootKey{kind: kind, ptr: ptr, slot: slot, detail: detail}
		if _, ok := seenGlobalRoots[key]; ok {
			return
		}
		seenGlobalRoots[key] = struct{}{}

		targetID, ok := g.FindObjectContaining(ptr)
		if !ok {
			a.Stats.UnresolvedGlobalRoots++
			return
		}
		globalRoots = append(globalRoots, RootRef{
			ObjectID: targetID,
			Ptr:      ptr,
			SlotAddr: slot,
			Kind:     kind,
			Detail:   detail,
		})
	}

	for _, r := range snap.Globals {
		resolveGlobalRoot(r.Kind, r.PointerAddr, r.Addr, r.Description)
	}
	for _, seg := range snap.Data {
		addSegmentGlobalRoots(seg, "data", snap.Params.PtrSize, resolveGlobalRoot)
	}
	for _, seg := range snap.BSS {
		addSegmentGlobalRoots(seg, "bss", snap.Params.PtrSize, resolveGlobalRoot)
	}
	for _, fin := range snap.Finalizers {
		ptr := fin.ObjectAddr
		if ptr == 0 {
			continue
		}
		targetID, ok := g.FindObjectContaining(ptr)
		if !ok {
			a.Stats.UnresolvedFinalizerRoots++
			continue
		}
		globalRoots = append(globalRoots, RootRef{
			ObjectID: targetID,
			Ptr:      ptr,
			Kind:     "finalizer",
		})
	}
	for _, fin := range snap.QueuedFinalizers {
		ptr := fin.ObjectAddr
		if ptr == 0 {
			continue
		}
		targetID, ok := g.FindObjectContaining(ptr)
		if !ok {
			a.Stats.UnresolvedFinalizerRoots++
			continue
		}
		globalRoots = append(globalRoots, RootRef{
			ObjectID: targetID,
			Ptr:      ptr,
			Kind:     "queued_finalizer",
		})
	}
	a.Globals.Roots = globalRoots
	a.Stats.GlobalRoots = len(globalRoots)

	a.Stats.UnresolvedPointers =
		a.Stats.UnresolvedObjectPointers +
			a.Stats.UnresolvedGoroutineRoots +
			a.Stats.UnresolvedGlobalRoots +
			a.Stats.UnresolvedFinalizerRoots

	a.Stats.Objects = len(g.Objects)
	var bytes uint64
	for i := range g.Objects {
		bytes += g.Objects[i].Size
	}
	a.Stats.ObjectBytes = bytes

	return a, nil
}

// ComputeReachability walks the graph from every goroutine's roots and
// from the global roots, filling GoroutineReachability.Reachable,
// GlobalReachability.Reachable, and the reach-derived counters in
// Stats (GoroutineReachableObjects, GlobalReachableObjects,
// SharedByGoroutinesObjects, UnreachableObjects).
//
// ComputeReachability is optional: Build no longer calls it. Callers
// that need whole-process reachability (diagnostics, offline analysis)
// should run it immediately after Build. The /debug/memusage handler
// skips this in favour of a single union BFS over only the goroutines
// whose labels match the request selector.
//
// Idempotent: running ComputeReachability twice yields the same sets
// and stats.
func ComputeReachability(a *Analysis) {
	if a == nil || a.Graph == nil {
		return
	}
	g := a.Graph

	for i := range a.Goroutines {
		a.Goroutines[i].Reachable = ReachableFrom(g, a.Goroutines[i].Roots)
	}
	a.Globals.Reachable = ReachableFrom(g, a.Globals.Roots)

	goroutineOwners := make(map[ObjectID]int, a.Stats.Objects)
	for _, gr := range a.Goroutines {
		for id := range gr.Reachable {
			goroutineOwners[id]++
		}
	}
	a.Stats.GoroutineReachableObjects = len(goroutineOwners)
	a.Stats.SharedByGoroutinesObjects = 0
	for _, n := range goroutineOwners {
		if n > 1 {
			a.Stats.SharedByGoroutinesObjects++
		}
	}
	a.Stats.GlobalReachableObjects = len(a.Globals.Reachable)

	allReach := make(map[ObjectID]struct{}, len(goroutineOwners)+len(a.Globals.Reachable))
	for id := range goroutineOwners {
		allReach[id] = struct{}{}
	}
	for id := range a.Globals.Reachable {
		allReach[id] = struct{}{}
	}
	unreachable := a.Stats.Objects - len(allReach)
	if unreachable < 0 {
		unreachable = 0
	}
	a.Stats.UnreachableObjects = unreachable
}

func addSegmentGlobalRoots(seg heapsnapshot.DataSegment, defaultKind string, ptrSize int, add func(kind string, ptr, slot uint64, detail string)) {
	kind := seg.Kind
	if kind == "" {
		kind = defaultKind
	}
	// Prefer parser-populated PointerSlots (parallel to PointerAddrs, always
	// correct even when zero-valued pointer fields precede non-zero ones).
	// Fall back to field-derived slots for test-constructed segments that do
	// not carry PointerSlots.
	var slots []uint64
	if len(seg.PointerSlots) == len(seg.PointerAddrs) {
		slots = seg.PointerSlots
	} else {
		slots = dataSegmentPointerSlots(seg, ptrSize)
	}
	for i, ptr := range seg.PointerAddrs {
		var slot uint64
		if i < len(slots) {
			slot = slots[i]
		}
		add(kind, ptr, slot, "")
	}
}

func dataSegmentPointerSlots(seg heapsnapshot.DataSegment, ptrSize int) []uint64 {
	if len(seg.Fields) == 0 {
		return nil
	}
	slots := make([]uint64, 0, len(seg.PointerAddrs))
	for _, f := range seg.Fields {
		switch f.Kind {
		case heapsnapshot.FieldKindPtr:
			slots = append(slots, seg.Addr+f.Offset)
		case heapsnapshot.FieldKindIface, heapsnapshot.FieldKindEface:
			slots = append(slots, seg.Addr+f.Offset+uint64(ptrSize))
		default:
			continue
		}
		if len(slots) == len(seg.PointerAddrs) {
			break
		}
	}
	return slots
}
