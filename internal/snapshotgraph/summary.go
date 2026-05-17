package snapshotgraph

import (
	"fmt"
	"io"
)

// PrintSummary writes a short, stable human-readable summary of the
// analysis. Selected-root reachability is computed by internal/memusage,
// not here.
func (a *Analysis) PrintSummary(w io.Writer) {
	if a == nil {
		fmt.Fprintln(w, "snapshot analysis: <nil>")
		return
	}
	s := a.Stats
	fmt.Fprintf(w, "objects: %d\n", s.Objects)
	fmt.Fprintf(w, "object bytes: %d\n", s.ObjectBytes)
	fmt.Fprintf(w, "object edges: %d\n", s.Edges)
	fmt.Fprintf(w, "resolved object edges: %d\n", s.Edges)
	fmt.Fprintf(w, "raw object pointers: %d\n", s.RawObjectPointers)
	fmt.Fprintf(w, "  zero object pointers: %d\n", s.ZeroObjectPointers)
	fmt.Fprintf(w, "  resolved object pointers: %d\n", s.ResolvedObjectPointers)
	fmt.Fprintf(w, "unresolved pointers: %d\n", s.UnresolvedPointers)
	fmt.Fprintf(w, "  unresolved object pointers: %d\n", s.UnresolvedObjectPointers)
	fmt.Fprintf(w, "  unresolved goroutine roots: %d\n", s.UnresolvedGoroutineRoots)
	fmt.Fprintf(w, "  unresolved global roots: %d\n", s.UnresolvedGlobalRoots)
	fmt.Fprintf(w, "  unresolved finalizer roots: %d\n", s.UnresolvedFinalizerRoots)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "goroutines: %d (system: %d)\n", s.Goroutines, s.SystemGoroutines)
	fmt.Fprintf(w, "goroutine roots: %d\n", s.GoroutineRootPointers)
	fmt.Fprintf(w, "goroutine-reachable objects: %d\n", s.GoroutineReachableObjects)
	fmt.Fprintf(w, "objects shared by multiple goroutines: %d\n", s.SharedByGoroutinesObjects)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "global roots: %d\n", s.GlobalRoots)
	fmt.Fprintf(w, "global-reachable objects: %d\n", s.GlobalReachableObjects)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "unreachable objects: %d\n", s.UnreachableObjects)
	fmt.Fprintf(w, "warnings: %d\n", len(a.Warnings))
	for _, msg := range a.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", msg)
	}
	fmt.Fprintln(w, "selected-root reachability: computed by internal/memusage")
}
