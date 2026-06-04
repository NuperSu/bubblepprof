package heapsnapshot

import (
	"fmt"
	"io"
)

// PrintSummary writes a short human-readable summary of the snapshot.
// It does not compute reachability or attribute bubbles.
func (s *HeapSnapshot) PrintSummary(w io.Writer) {
	if s == nil {
		fmt.Fprintln(w, "heap snapshot: <nil>")
		return
	}

	fmt.Fprintf(w, "heap dump header: %s\n", s.Header)
	fmt.Fprintf(w, "go build version: %s\n", s.Params.BuildVersion)
	fmt.Fprintf(w, "goarch: %s\n", s.Params.GOARCH)
	fmt.Fprintf(w, "ptr size: %d\n", s.Params.PtrSize)
	fmt.Fprintf(w, "big endian: %t\n", s.Params.BigEndian)
	fmt.Fprintf(w, "heap range: 0x%x..0x%x\n", s.Params.HeapStart, s.Params.HeapEnd)
	fmt.Fprintf(w, "num cpu: %d\n", s.Params.NumCPU)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "objects: %d\n", s.Stats.ObjectCount)
	fmt.Fprintf(w, "object bytes: %d\n", s.Stats.ObjectBytes)
	fmt.Fprintf(w, "object pointer fields: %d\n", s.Stats.ObjectPointers)
	fmt.Fprintf(w, "types: %d\n", s.Stats.TypeCount)
	fmt.Fprintf(w, "itabs: %d\n", s.Stats.ItabCount)
	fmt.Fprintf(w, "goroutines: %d\n", s.Stats.GoroutineCount)
	fmt.Fprintf(w, "stack frames: %d\n", s.Stats.StackFrameCount)
	fmt.Fprintf(w, "stack root pointers: %d\n", s.Stats.StackPointers)
	fmt.Fprintf(w, "other roots: %d\n", s.Stats.OtherRootCount)
	fmt.Fprintf(w, "data segments: %d\n", s.Stats.DataCount)
	fmt.Fprintf(w, "data root pointers: %d\n", s.Stats.DataPointers)
	fmt.Fprintf(w, "bss segments: %d\n", s.Stats.BSSCount)
	fmt.Fprintf(w, "bss root pointers: %d\n", s.Stats.BSSPointers)
	fmt.Fprintf(w, "global roots: %d\n", s.Stats.GlobalRootCount)
	fmt.Fprintf(w, "finalizers: %d\n", s.Stats.FinalizerCount)
	fmt.Fprintf(w, "queued finalizers: %d\n", s.Stats.QueuedFinalizers)
	fmt.Fprintf(w, "os threads: %d\n", s.Stats.OSThreadCount)
	fmt.Fprintf(w, "defers: %d\n", s.Stats.DeferCount)
	fmt.Fprintf(w, "panics: %d\n", s.Stats.PanicCount)
	fmt.Fprintf(w, "mem prof entries: %d\n", s.Stats.MemProfCount)
	fmt.Fprintf(w, "alloc samples: %d\n", s.Stats.AllocSampleCount)
	fmt.Fprintf(w, "unknown records: %d\n", s.Stats.UnknownRecords)
	fmt.Fprintf(w, "interface fields decoded: %d\n", s.Stats.InterfaceFieldsDecoded)
	fmt.Fprintf(w, "eface fields decoded: %d\n", s.Stats.EfaceFieldsDecoded)
	fmt.Fprintf(w, "warnings: %d\n", len(s.Warnings))
	for _, msg := range s.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", msg)
	}
}
