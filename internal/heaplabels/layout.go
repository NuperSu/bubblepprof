package heaplabels

import (
	"bubblepprof/internal/addrspace"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/runtimelayout"
)

const (
	DefaultMaxLabels    = 1024
	DefaultMaxStringLen = 1 << 20
)

// Options tunes decoding limits applied to every goroutine. Runtime
// layout (offsets / pointer width) is supplied separately as a
// runtimelayout.Layout argument.
type Options struct {
	MaxLabels    uint64
	MaxStringLen uint64

	// ExtraMemory is an optional secondary address-space reader
	// consulted when heap dump object contents do not cover the
	// requested address. The decoder always tries heap memory first
	// so structural reads (pointers, slice headers) see the
	// stop-the-world snapshot exactly; ExtraMemory only fills in
	// string bytes that live outside heap objects, typically pprof
	// label string literals in the executable's read-only data.
	//
	// Set to addrspace.ProcessReader for the in-process
	// /debug/memusage handler, or addrspace.ELFReader for offline
	// `snapshot heap-labels --exe`. Nil disables the fallback.
	ExtraMemory addrspace.Reader
}

// LookupInputFromSnapshot extracts the runtime-layout lookup key from a
// parsed heap snapshot. Callers pass the result to runtimelayout.Lookup
// (or runtimelayout.Manual for debug CLIs).
func LookupInputFromSnapshot(snap *heapsnapshot.HeapSnapshot) runtimelayout.LookupInput {
	if snap == nil {
		return runtimelayout.LookupInput{}
	}
	return runtimelayout.LookupInput{
		GoVersion: snap.Params.BuildVersion,
		GOARCH:    snap.Params.GOARCH,
		PtrSize:   snap.Params.PtrSize,
		BigEndian: snap.Params.BigEndian,
	}
}

// LookupLayout resolves a heap snapshot's runtime layout from the
// verified-runtime table. Returns (zero, false) when the runtime is
// unsupported; callers must surface unsupported_runtime rather than
// fabricate a layout.
func LookupLayout(snap *heapsnapshot.HeapSnapshot) (runtimelayout.Layout, bool) {
	return runtimelayout.Lookup(LookupInputFromSnapshot(snap))
}

func normalizeOptions(opts Options) Options {
	if opts.MaxLabels == 0 {
		opts.MaxLabels = DefaultMaxLabels
	}
	if opts.MaxStringLen == 0 {
		opts.MaxStringLen = DefaultMaxStringLen
	}
	return opts
}
