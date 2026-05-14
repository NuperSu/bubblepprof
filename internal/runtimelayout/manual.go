package runtimelayout

import "fmt"

// Manual constructs a Layout from an explicit runtime.g.labels offset.
//
// Manual is the only layout source allowed when a debug CLI flag or the
// labeloffsetprobe supplies a candidate offset. It must not be used by
// /debug/memusage, which has to refuse rather than guess.
//
// Phase 2 only supports 64-bit little-endian targets; other widths return
// an error so callers fail loudly instead of silently producing a wrong
// layout.
func Manual(input LookupInput, gLabelsOffset uint64) (Layout, error) {
	if input.PtrSize != 8 {
		return Layout{}, fmt.Errorf(
			"manual runtime layout: unsupported pointer size %d (only 8 is implemented)",
			input.PtrSize,
		)
	}
	if input.BigEndian {
		return Layout{}, fmt.Errorf("manual runtime layout: big-endian targets are not implemented")
	}
	layout := with64BitLittleEndianDefaults(Layout{
		Source:        SourceManual,
		GoVersion:     input.GoVersion,
		GOARCH:        input.GOARCH,
		GLabelsOffset: gLabelsOffset,
		Description:   "manual runtime.g.labels offset",
	})
	return layout, nil
}
