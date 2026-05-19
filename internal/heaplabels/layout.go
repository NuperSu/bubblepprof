package heaplabels

import (
	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
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

	// ExtraStringMemory is an optional secondary address-space reader
	// consulted only when reading string body bytes (key or value
	// characters) that live outside heap dump object contents. The
	// decoder uses the heap-only reader for all structural reads
	// (runtime.g, labelMap, slice headers, string headers);
	// ExtraStringMemory is consulted solely for the raw bytes that
	// follow a located string header.
	//
	// Typical use: set to addrspace.ProcessReader for the in-process
	// /debug/memusage handler so ordinary pprof.Labels("job","42")
	// string literals (which live in executable read-only data) are
	// recovered. Nil disables the fallback.
	ExtraStringMemory addrspace.Reader

	// HeapMemory, when non-nil, replaces the Memory that DecodeAll would
	// otherwise build internally from snap.Objects. Use this with the
	// lazy parse path (heapdump.ParseLazyContents) so structural reads
	// hit a ContentResolver-backed Memory instead of materialized object
	// content bytes.
	HeapMemory *Memory
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
