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
	// PRE-RELEASE: verified on go1.27-devel_e62d3e6e (64-bit LE offset 0x160)
	// on linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64,
	// and freebsd/amd64 via CI tip jobs (not required). This entry may need
	// updating if runtime.g struct changes before the final release. Remove
	// this comment once go1.27.0 ships and the offset is confirmed against
	// the release build.
	{
		VersionPrefix: "go1.27-devel",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x160,
			Description:   "pre-release go1.27-devel 64-bit LE runtime.g.labels offset 0x160 (verified on commit e62d3e6e)",
		}),
	},
	// PRE-RELEASE: verified on go1.27-devel_e62d3e6e (32-bit LE offset 0xd8)
	// on linux/386 and linux/arm/v7 via CI tip jobs (not required).
	// Subject to change before go1.27 final release; see 64-bit entry above.
	{
		VersionPrefix: "go1.27-devel",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd8,
			Description:   "pre-release go1.27-devel 32-bit LE runtime.g.labels offset 0xd8 (verified on commit e62d3e6e)",
		}),
	},
	// Verified on linux/amd64, linux/arm64 (go1.26.0–go1.26.3),
	// darwin/amd64, darwin/arm64, windows/amd64 (go1.26.0–go1.26.3),
	// and freebsd/amd64, freebsd/arm64 (stable, go1.26.*).
	// Applies to all 64-bit little-endian platforms for go1.26.*.
	{
		VersionPrefix: "go1.26.",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x160,
			Description:   "verified go1.26.* 64-bit LE (linux, darwin, windows, freebsd) runtime.g.labels offset 0x160",
		}),
	},
	// Verified on linux/arm/v7 (go1.26.0–go1.26.3), linux/386 (go1.26.0–go1.26.3),
	// and freebsd/386 (stable, go1.26.*).
	// Applies to all 32-bit little-endian platforms for go1.26.*.
	{
		VersionPrefix: "go1.26.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd8,
			Description:   "verified go1.26.* 32-bit LE (arm, 386, freebsd/386) runtime.g.labels offset 0xd8",
		}),
	},
	// Verified on linux/amd64, linux/arm64 (go1.25.0–go1.25.10),
	// darwin/amd64, darwin/arm64, windows/amd64 (go1.25.0–go1.25.10),
	// and freebsd/amd64, freebsd/arm64 (stable, go1.25.*).
	// Applies to all 64-bit little-endian platforms for go1.25.*.
	{
		VersionPrefix: "go1.25.",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0x158,
			Description:   "verified go1.25.* 64-bit LE (linux, darwin, windows, freebsd) runtime.g.labels offset 0x158",
		}),
	},
	// Verified on linux/arm/v7 (go1.25.0–go1.25.10), linux/386 (go1.25.0–go1.25.10),
	// and freebsd/386 (stable, go1.25.*).
	// Applies to all 32-bit little-endian platforms for go1.25.*.
	{
		VersionPrefix: "go1.25.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd0,
			Description:   "verified go1.25.* 32-bit LE (arm, 386, freebsd/386) runtime.g.labels offset 0xd0",
		}),
	},
	// Verified on linux/amd64, linux/arm64 (go1.24.0–go1.24.13),
	// darwin/amd64, darwin/arm64, windows/amd64 (go1.24.0–go1.24.13),
	// and freebsd/amd64, freebsd/arm64 (stable, go1.24.*).
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
			Description:   "verified go1.24.* 64-bit LE (linux, darwin, windows, freebsd) runtime.g.labels offset 0x160",
		}),
	},
	// Verified on linux/arm/v7 (go1.24.0–go1.24.13), linux/386 (go1.24.0–go1.24.13),
	// and freebsd/386 (stable, go1.24.*).
	// Applies to all 32-bit little-endian platforms for go1.24.*.
	{
		VersionPrefix: "go1.24.",
		PtrSize:       4,
		BigEndian:     false,
		Layout: with32BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GLabelsOffset: 0xd4,
			Description:   "verified go1.24.* 32-bit LE (arm, 386, freebsd/386) runtime.g.labels offset 0xd4",
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
// BigEndian, ignoring GoVersion. Intended for development tools such as
// cmd/labeloffsetprobe and tests; never call this from the HTTP path because
// the layout may be wrong and will produce silent incorrect results.
// Returns (zero, false) when no width/endian match exists.
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
