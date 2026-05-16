// Package runtimelayout describes the private Go runtime layout that
// heap-native pprof label recovery depends on, and resolves it from a
// small verified table.
//
// The runtime field offsets used to decode runtime.g.labels are Go-version
// and GOARCH specific. Callers (heaplabels, /debug/memusage, debug CLI
// tools) consume a Layout value through Lookup or Manual; they never embed
// layout knowledge directly.
//
// DWARF/ELF/process-memory based layout discovery is reserved for a later
// phase. This package only resolves verified table entries and explicit
// manual offsets.
package runtimelayout

import "encoding/binary"

// Source describes where a Layout came from. /debug/memusage accepts
// SourceTable only; debug CLI commands may also use SourceManual. SourceDWARF
// is reserved for a future phase.
type Source string

const (
	// SourceTable means the layout was selected from the verified-runtime
	// table. The endpoint can trust the offsets to be correct for the
	// matched Go version.
	SourceTable Source = "table"

	// SourceManual means the layout was constructed from an explicit
	// runtime.g.labels offset supplied by a debug CLI flag or test. It is
	// not safe to use in normal /debug/memusage flows.
	SourceManual Source = "manual"

	// SourceDWARF is reserved for a future runtime-layout provider that
	// recovers offsets from DWARF debug info. Not implemented in Phase 2.
	SourceDWARF Source = "dwarf"
)

// Layout captures every offset / width that heaplabels needs to decode a
// pprof labelMap out of a heap-dump goroutine record.
//
// The header offsets (slice, string, label) follow Go's standard memory
// layout for 64-bit little-endian platforms. Other widths can be added
// once verified.
type Layout struct {
	Source Source

	GoVersion string
	GOARCH    string
	PtrSize   int
	BigEndian bool

	// GLabelsOffset is the byte offset of runtime.g.labels from the
	// beginning of the runtime.g struct.
	GLabelsOffset uint64

	// LabelMapSetOffset is the offset of the embedded label.Set within
	// runtime/pprof.labelMap. Currently 0 in all supported runtimes.
	LabelMapSetOffset uint64

	// SetListOffset is the offset of label.Set.List (the []label.Label
	// slice) within label.Set. Currently 0 in all supported runtimes.
	SetListOffset uint64

	// Slice header layout (data uintptr, len uintptr, cap uintptr).
	SliceDataOffset uint64
	SliceLenOffset  uint64
	SliceCapOffset  uint64

	// String header layout (data uintptr, len uintptr).
	StringDataOffset uint64
	StringLenOffset  uint64

	// internal/runtime/pprof/label.Label layout (Key string, Value string).
	LabelSize        uint64
	LabelKeyOffset   uint64
	LabelValueOffset uint64

	// Description is a short human-readable explanation that the debug
	// CLI prints alongside the offset.
	Description string
}

// ByteOrder returns binary.LittleEndian or binary.BigEndian as configured
// by BigEndian.
func (l Layout) ByteOrder() binary.ByteOrder {
	if l.BigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// LookupInput is the small request bundle used by Lookup. Callers fill it
// from a parsed heap snapshot (snap.Params) or, for in-process probes,
// from runtime.Version() / runtime.GOARCH.
type LookupInput struct {
	GoVersion string
	GOARCH    string
	PtrSize   int
	BigEndian bool
}

// with64BitLittleEndianDefaults fills the slice/string/Label offsets that
// are common to every 64-bit little-endian Go runtime currently in scope.
// Verified table entries and Manual layouts both go through this helper so
// the constants are defined exactly once.
func with64BitLittleEndianDefaults(l Layout) Layout {
	return withLittleEndianDefaults(l, 8)
}

// with32BitLittleEndianDefaults is the 32-bit equivalent of
// with64BitLittleEndianDefaults. Slice/string/label headers are the same
// structure but every pointer and int field is 4 bytes wide.
func with32BitLittleEndianDefaults(l Layout) Layout {
	return withLittleEndianDefaults(l, 4)
}

func withLittleEndianDefaults(l Layout, ptrSize int) Layout {
	ptr := uint64(ptrSize)
	l.PtrSize = ptrSize
	l.BigEndian = false
	l.SliceDataOffset = 0
	l.SliceLenOffset = ptr
	l.SliceCapOffset = 2 * ptr
	l.StringDataOffset = 0
	l.StringLenOffset = ptr
	l.LabelKeyOffset = 0
	l.LabelValueOffset = 2 * ptr
	l.LabelSize = 4 * ptr
	return l
}
