package snapshotgraph

import (
	"fmt"
	"math"
	"sort"

	"bubblepprof/internal/heapsnapshot"
)

// Build converts a parsed HeapSnapshot into an Analysis. It resolves
// pointer values to object IDs (supporting interior pointers), builds
// graph edges with deduplication, extracts goroutine and global roots,
// and computes per-goroutine and process-global reachability.
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
	if iface, eface := snap.Stats.InterfaceFieldsSkipped, snap.Stats.EfaceFieldsSkipped; iface+eface > 0 {
		a.Warnings = append(a.Warnings,
			fmt.Sprintf("parse: %d interface and %d eface fields were preserved but not decoded into graph edges",
				iface, eface))
	}

	zeroAddrWarned := false
	zeroSizedWarned := false
	for i := range snap.Objects {
		src := &snap.Objects[i]

		// Address 0 cannot be used for lookup (would alias with nil
		// pointers). Keep the object in the graph for completeness, but
		// do not add it to ByAddr or ranges.
		if src.Addr == 0 {
			id := ObjectID(len(g.Objects))
			ptrs := append([]uint64(nil), src.PointerAddrs...)
			g.Objects = append(g.Objects, Object{
				ID:           id,
				Addr:         0,
				Size:         src.Size,
				PointerAddrs: ptrs,
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
			continue
		}

		id := ObjectID(len(g.Objects))
		ptrs := append([]uint64(nil), src.PointerAddrs...)
		g.Objects = append(g.Objects, Object{
			ID:           id,
			Addr:         src.Addr,
			Size:         src.Size,
			PointerAddrs: ptrs,
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
	for i := 1; i < len(g.ranges); i++ {
		prev, cur := g.ranges[i-1], g.ranges[i]
		if cur.Start < prev.End {
			a.Warnings = append(a.Warnings,
				fmt.Sprintf("overlapping object ranges: 0x%x..0x%x and 0x%x..0x%x",
					prev.Start, prev.End, cur.Start, cur.End))
			if opts.Strict {
				return nil, fmt.Errorf("overlapping object ranges at 0x%x", cur.Start)
			}
		}
	}

	for i := range g.Objects {
		obj := &g.Objects[i]
		for _, ptr := range obj.PointerAddrs {
			a.Stats.RawObjectPointers++
			if ptr == 0 {
				continue
			}
			targetID, ok := g.FindObjectContaining(ptr)
			if !ok {
				a.Stats.UnresolvedObjectPointers++
				continue
			}
			a.Stats.ResolvedObjectPointers++
			g.AddEdge(obj.ID, targetID)
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
			for _, ptr := range fr.PointerAddrs {
				if ptr == 0 {
					continue
				}
				targetID, ok := g.FindObjectContaining(ptr)
				if !ok {
					a.Stats.UnresolvedGoroutineRoots++
					continue
				}
				roots = append(roots, RootRef{
					ObjectID: targetID,
					Ptr:      ptr,
					Kind:     "stack",
					Detail:   fr.FuncName,
				})
			}
		}
		a.Goroutines = append(a.Goroutines, GoroutineReachability{
			GoroutineID: gr.ID,
			Roots:       roots,
		})
		a.Stats.GoroutineRoots += len(roots)
	}
	a.Stats.Goroutines = len(a.Goroutines)

	var globalRoots []RootRef
	for _, r := range snap.Globals {
		ptr := r.PointerAddr
		if ptr == 0 {
			continue
		}
		targetID, ok := g.FindObjectContaining(ptr)
		if !ok {
			a.Stats.UnresolvedGlobalRoots++
			continue
		}
		kind := r.Kind
		if kind == "" {
			kind = "otherroot"
		}
		globalRoots = append(globalRoots, RootRef{
			ObjectID: targetID,
			Ptr:      ptr,
			SlotAddr: r.Addr,
			Kind:     kind,
			Detail:   r.Description,
		})
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

	for i := range a.Goroutines {
		a.Goroutines[i].Reachable = ReachableFrom(g, a.Goroutines[i].Roots)
	}
	a.Globals.Reachable = ReachableFrom(g, a.Globals.Roots)

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

	goroutineOwners := make(map[ObjectID]int, a.Stats.Objects)
	for _, gr := range a.Goroutines {
		for id := range gr.Reachable {
			goroutineOwners[id]++
		}
	}
	a.Stats.GoroutineReachableObjects = len(goroutineOwners)
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

	return a, nil
}
