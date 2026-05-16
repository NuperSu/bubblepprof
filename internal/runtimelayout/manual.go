package runtimelayout

import "fmt"

// Manual constructs a Layout from an explicit runtime.g.labels offset.
//
// Manual is the only layout source allowed when a debug CLI flag or the
// labeloffsetprobe supplies a candidate offset. It must not be used by
// /debug/memusage, which has to refuse rather than guess.
//
// Supported: little-endian, pointer size 4 or 8. Big-endian and other
// pointer sizes return an error.
func Manual(input LookupInput, gLabelsOffset uint64) (Layout, error) {
	if input.PtrSize != 4 && input.PtrSize != 8 {
		return Layout{}, fmt.Errorf(
			"manual runtime layout: unsupported pointer size %d (only 4 and 8 are implemented)",
			input.PtrSize,
		)
	}
	if input.BigEndian {
		return Layout{}, fmt.Errorf("manual runtime layout: big-endian targets are not implemented")
	}
	base := Layout{
		Source:        SourceManual,
		GoVersion:     input.GoVersion,
		GOARCH:        input.GOARCH,
		GLabelsOffset: gLabelsOffset,
		Description:   "manual runtime.g.labels offset",
	}
	var layout Layout
	if input.PtrSize == 4 {
		layout = with32BitLittleEndianDefaults(base)
	} else {
		layout = with64BitLittleEndianDefaults(base)
	}
	return layout, nil
}
