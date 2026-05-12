package snapshotgraph

import (
	"fmt"
	"io"
)

// PrintSummary writes a short, stable human-readable summary of the
// analysis. It is intentionally bubble-agnostic — Phase 4 does not
// attribute bubbles.
func (a *Analysis) PrintSummary(w io.Writer) {
	if a == nil {
		fmt.Fprintln(w, "snapshot analysis: <nil>")
		return
	}
	s := a.Stats
	fmt.Fprintf(w, "objects: %d\n", s.Objects)
	fmt.Fprintf(w, "object bytes: %d\n", s.ObjectBytes)
	fmt.Fprintf(w, "object edges: %d\n", s.Edges)
	fmt.Fprintf(w, "raw object pointers: %d\n", s.RawObjectPointers)
	fmt.Fprintf(w, "resolved object edges: %d\n", s.ResolvedObjectEdges)
	fmt.Fprintf(w, "unresolved pointers: %d\n", s.UnresolvedPointers)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "goroutines: %d\n", s.Goroutines)
	fmt.Fprintf(w, "goroutine roots: %d\n", s.GoroutineRoots)
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
	fmt.Fprintln(w, "bubble attribution: not implemented in this phase")
}
