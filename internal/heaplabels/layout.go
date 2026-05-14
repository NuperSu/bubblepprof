package heaplabels

import (
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
