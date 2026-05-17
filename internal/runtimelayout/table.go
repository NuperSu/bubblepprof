package runtimelayout

import "fmt"

// TableEntry is one verified runtime layout. A match requires
// (VersionPrefix, PtrSize, BigEndian) to agree with the input;
// version matching uses a strict prefix so build-info suffixes such as
// "go1.26.3-X:nodwarf5" still resolve to the right entry.
//
// GOARCH is intentionally absent: runtime.g is a plain Go struct whose field
// offsets depend only on pointer width and Go version, not on architecture.
// All 64-bit LE platforms share one entry per Go version; all 32-bit LE
// platforms share another. The input GOARCH is echoed into Layout.GOARCH
// for diagnostics but never compared during lookup.
type TableEntry struct {
	VersionPrefix string
	PtrSize       int
	BigEndian     bool
	Layout        Layout
}

// verifiedTable lists every runtime layout this prototype has been
// verified against. The verifier is cmd/labeloffsetprobe.
//
// Each entry covers all architectures that share the same (PtrSize, BigEndian)
// for a given Go version — runtime.g field offsets depend only on pointer width
// and Go version, not on the specific architecture. Add a new entry only after
// running the probe and confirming the offset against a heap dump produced by a
// live binary on that runtime.
var verifiedTable = []TableEntry{
	// Verified on linux/amd64 (go1.26.0–go1.26.3) and linux/arm64 (go1.26.0–go1.26.3).
	// Applies to all 64-bit little-endian platforms for go1.26.*.
	{
		VersionPrefix: "go1.26.",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x160,
			Description:   "verified go1.26.* 64-bit LE (amd64, arm64) runtime.g.labels offset 0x160",
		}),
	},
	// Verified on linux/arm/v7 (go1.26.3); suggested for linux/386 (struct layout analysis).
	// Applies to all 32-bit little-endian platforms for go1.26.*.
	{
		VersionPrefix: "go1.26.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd8,
			Description:   "verified go1.26.* 32-bit LE (arm); suggested for 386. runtime.g.labels offset 0xd8",
		}),
	},
	// Verified on linux/amd64 (go1.25.0–go1.25.10) and linux/arm64 (go1.25.0–go1.25.10).
	// Applies to all 64-bit little-endian platforms for go1.25.*.
	{
		VersionPrefix: "go1.25.",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x158,
			Description:   "verified go1.25.* 64-bit LE (amd64, arm64) runtime.g.labels offset 0x158",
		}),
	},
	// Verified on linux/arm/v7 (go1.25); suggested for linux/386 (struct layout analysis).
	// Applies to all 32-bit little-endian platforms for go1.25.*.
	{
		VersionPrefix: "go1.25.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd0,
			Description:   "verified go1.25.* 32-bit LE (arm); suggested for 386. runtime.g.labels offset 0xd0",
		}),
	},
	// Verified on linux/amd64 (go1.24.0–go1.24.13) and linux/arm64 (go1.24.0–go1.24.13).
	// go1.24 introduced internal/runtime/pprof and the struct-based label format
	// ([]label.Label). go1.23 and earlier used map[string]string and are not supported.
	// Applies to all 64-bit little-endian platforms for go1.24.*.
	{
		VersionPrefix: "go1.24.",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x160,
			Description:   "verified go1.24.* 64-bit LE (amd64, arm64) runtime.g.labels offset 0x160",
		}),
	},
	// Verified on linux/arm/v7 (go1.24); suggested for linux/386 (struct layout analysis).
	// Applies to all 32-bit little-endian platforms for go1.24.*.
	{
		VersionPrefix: "go1.24.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd4,
			Description:   "verified go1.24.* 32-bit LE (arm); suggested for 386. runtime.g.labels offset 0xd4",
		}),
	},
}

// Lookup returns the verified runtime layout that matches the input, or
// (zero, false) when no table entry applies. Lookup never guesses an
// offset; callers that get false must surface unsupported_runtime instead
// of trying a candidate.
func Lookup(input LookupInput) (Layout, bool) {
	if input.GoVersion == "" {
		return Layout{}, false
	}
	for _, e := range verifiedTable {
		if e.PtrSize != input.PtrSize {
			continue
		}
		if e.BigEndian != input.BigEndian {
			continue
		}
		if !hasVersionPrefix(input.GoVersion, e.VersionPrefix) {
			continue
		}
		layout := e.Layout
		layout.GoVersion = input.GoVersion
		layout.GOARCH = input.GOARCH
		return layout, true
	}
	return Layout{}, false
}

// LookupBestEffort returns the first table entry that matches PtrSize and
// BigEndian, ignoring GoVersion. It is used when AllowInferredLayout is
// set: the caller gets a best-effort layout for an unverified Go version and
// must surface a warning. Returns (zero, false) when no width/endian match exists.
func LookupBestEffort(input LookupInput) (Layout, bool) {
	for _, e := range verifiedTable {
		if e.PtrSize != input.PtrSize {
			continue
		}
		if e.BigEndian != input.BigEndian {
			continue
		}
		layout := e.Layout
		layout.GoVersion = input.GoVersion
		layout.GOARCH = input.GOARCH
		return layout, true
	}
	return Layout{}, false
}

// UnsupportedMessage formats a stable diagnostic for callers that need to
// explain why Lookup returned false. The wording is shared by the HTTP
// endpoint and the offline CLI so logs/tests can match on it.
func UnsupportedMessage(input LookupInput) string {
	endian := "little"
	if input.BigEndian {
		endian = "big"
	}
	return fmt.Sprintf(
		"unsupported Go runtime layout: go version %q, goarch %q, ptr size %d, %s endian",
		input.GoVersion, input.GOARCH, input.PtrSize, endian,
	)
}

func hasVersionPrefix(version, prefix string) bool {
	if len(version) < len(prefix) {
		return false
	}
	return version[:len(prefix)] == prefix
}
