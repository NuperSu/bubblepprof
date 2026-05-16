package runtimelayout

import "fmt"

// TableEntry is one verified runtime layout. A match requires every field
// (VersionPrefix, GOARCH, PtrSize, BigEndian) to agree with the input;
// version matching uses a strict prefix so build-info suffixes such as
// "go1.26.3-X:nodwarf5" still resolve to the right entry.
type TableEntry struct {
	VersionPrefix string
	GOARCH        string
	PtrSize       int
	BigEndian     bool
	Layout        Layout
}

// verifiedTable lists every runtime layout this prototype has been
// verified against. The verifier is cmd/labeloffsetprobe.
//
// Add a new entry only after running the probe and confirming the offset
// against a heap dump produced by a live binary on that runtime.
var verifiedTable = []TableEntry{
	// Verified with cmd/labeloffsetprobe on linux/amd64
	// (go version go1.26.3-X:nodwarf5 linux/amd64).
	{
		VersionPrefix: "go1.26.",
		GOARCH:        "amd64",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GOARCH:        "amd64",
			GLabelsOffset: 0x160,
			Description:   "verified go1.26.* amd64 runtime.g.labels layout",
		}),
	},
	// Verified with cmd/labeloffsetprobe on linux/amd64
	// (go version go1.25.0 linux/amd64).
	{
		VersionPrefix: "go1.25.",
		GOARCH:        "amd64",
		PtrSize:       8,
		BigEndian:     false,
		Layout: with64BitLittleEndianDefaults(Layout{
			Source:        SourceTable,
			GOARCH:        "amd64",
			GLabelsOffset: 0x158,
			Description:   "verified go1.25.* amd64 runtime.g.labels layout",
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
		if e.GOARCH != input.GOARCH {
			continue
		}
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
		return layout, true
	}
	return Layout{}, false
}

// LookupBestEffort returns the first table entry that matches GOARCH, PtrSize,
// and BigEndian, ignoring GoVersion. It is used when AllowInferredLayout is
// set: the caller gets a best-effort layout for an unverified Go version and
// must surface a warning. Returns (zero, false) when no arch/width match exists.
func LookupBestEffort(input LookupInput) (Layout, bool) {
	for _, e := range verifiedTable {
		if e.GOARCH != input.GOARCH {
			continue
		}
		if e.PtrSize != input.PtrSize {
			continue
		}
		if e.BigEndian != input.BigEndian {
			continue
		}
		layout := e.Layout
		layout.GoVersion = input.GoVersion
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
