// Package snapshotgraph turns a parsed heapsnapshot.HeapSnapshot into a
// compact process-wide object graph with per-goroutine and process-global
// reachability sets.
//
// The package does not depend on Delve and does not parse heap dumps itself.
// It only resolves raw pointer addresses produced by the heap dump parser
// into object IDs, builds graph edges, and walks the graph.
package snapshotgraph

import "sort"

// ObjectID identifies one object in a Graph. It is also the object's index
// in Graph.Objects.
type ObjectID uint32

// Object is one heap object in the resolved graph.
type Object struct {
	ID       ObjectID
	Addr     uint64
	Size     uint64
	Children []ObjectID

	// PointerAddrs is the original list of decoded pointer values from the
	// parser, kept for debugging. The graph edges live in Children.
	PointerAddrs []uint64
}

// ObjectRange is one half-open address range [Start, End) covered by a
// single object. Used for interior-pointer resolution. Zero-sized objects
// are intentionally not registered as ranges.
type ObjectRange struct {
	Start uint64
	End   uint64
	ID    ObjectID
}

// Graph holds the compact process-wide object graph.
type Graph struct {
	Objects []Object
	ByAddr  map[uint64]ObjectID

	// ranges is sorted by Start. Used by FindObjectContaining for interior
	// pointer lookups. Zero-sized objects are not in ranges.
	ranges []ObjectRange
}

// validID reports whether id is a valid index into g.Objects.
func (g *Graph) validID(id ObjectID) bool {
	return g != nil && int(id) < len(g.Objects)
}

// FindObjectContaining resolves a non-zero pointer value to the object
// that contains it. The lookup supports interior pointers: a pointer
// somewhere inside an object's payload still resolves to that object.
// Address zero is treated as nil and never resolves.
func (g *Graph) FindObjectContaining(ptr uint64) (ObjectID, bool) {
	if g == nil || ptr == 0 {
		return 0, false
	}
	if id, ok := g.ByAddr[ptr]; ok {
		return id, true
	}
	if len(g.ranges) == 0 {
		return 0, false
	}
	idx := sort.Search(len(g.ranges), func(i int) bool {
		return g.ranges[i].Start > ptr
	})
	if idx == 0 {
		return 0, false
	}
	r := g.ranges[idx-1]
	if ptr < r.End {
		return r.ID, true
	}
	return 0, false
}

// AddEdge records a child edge from -> to. Duplicate edges are dropped.
// Self-edges are allowed. Returns true when a new edge was actually
// appended. Returns false on invalid IDs (no panic) or duplicates.
func (g *Graph) AddEdge(from, to ObjectID) bool {
	if !g.validID(from) || !g.validID(to) {
		return false
	}
	children := g.Objects[from].Children
	for _, existing := range children {
		if existing == to {
			return false
		}
	}
	g.Objects[from].Children = append(children, to)
	return true
}

// RootRef is one resolved pointer from a stack frame, global root, or
// finalizer record into a heap object.
type RootRef struct {
	ObjectID ObjectID
	Ptr      uint64
	SlotAddr uint64
	Kind     string
	Detail   string
}

// GoroutineReachability is the reachability set rooted at a single
// goroutine's stack roots.
type GoroutineReachability struct {
	GoroutineID uint64
	Roots       []RootRef
	Reachable   map[ObjectID]struct{}
}

// GlobalReachability is the reachability set rooted at process-wide
// roots: data, bss, otherroot, finalizer, queued_finalizer.
type GlobalReachability struct {
	Roots     []RootRef
	Reachable map[ObjectID]struct{}
}

// Analysis is the full output of Build: a resolved object graph plus
// reachability sets and summary stats.
type Analysis struct {
	Graph      *Graph
	Goroutines []GoroutineReachability
	Globals    GlobalReachability

	Warnings []string
	Stats    Stats
}

// Stats aggregates counters across the analysis.
//
// Pointer accounting:
//
//	RawObjectPointers      = total non-zero pointers seen in object slots
//	ResolvedObjectPointers = those that resolved to a heap object (pre-dedup)
//	Edges                  = deduplicated child edges in the graph
//	UnresolvedObjectPointers + ResolvedObjectPointers = RawObjectPointers
//	(minus skipped iface/eface, which is tracked at the parser level)
//
// Root accounting tracks unresolved root pointers separately by source
// category so it is clear whether missing reachability is due to objects,
// goroutine stacks, globals, or finalizers.
type Stats struct {
	Objects                int
	ObjectBytes            uint64
	Edges                  int
	RawObjectPointers      int
	ResolvedObjectPointers int

	UnresolvedPointers       int
	UnresolvedObjectPointers int
	UnresolvedGoroutineRoots int
	UnresolvedGlobalRoots    int
	UnresolvedFinalizerRoots int

	Goroutines     int
	GoroutineRoots int
	GlobalRoots    int

	GoroutineReachableObjects int
	GlobalReachableObjects    int
	UnreachableObjects        int

	SharedByGoroutinesObjects int
}

// Options tunes the builder. Strict turns warnings into errors.
type Options struct {
	Strict bool
}
