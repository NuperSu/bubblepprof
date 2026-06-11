// Package bundle implements the external-analyser capture artifact: a
// tar stream containing a heap dump, a snapshot of the target's
// read-only memory segments (so literal pprof label strings can be
// recovered out of process), and metadata. The target process produces
// a bundle cheaply (no parsing); the analyser CLI feeds it into the
// same memusage.AnalyzeDump pipeline used by the in-process endpoint.
package bundle

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FormatVersion is the bundle format produced by this package. Readers
// reject bundles with a greater version; unknown extra tar members are
// ignored for forward compatibility within a version.
const FormatVersion = 1

// Tar member names. heap.dump is emitted last (largest member);
// meta.json first so consumers can inspect a bundle cheaply. Readers
// must not rely on member order.
const (
	MetaMember     = "meta.json"
	SegmentsMember = "rodata/segments.json"
	HeapDumpMember = "heap.dump"

	segmentMemberPrefix = "rodata/"
	segmentMemberSuffix = ".bin"
)

// segmentMemberName returns the tar member name for segment index i,
// e.g. "rodata/00000.bin".
func segmentMemberName(i int) string {
	return fmt.Sprintf("%s%05d%s", segmentMemberPrefix, i, segmentMemberSuffix)
}

// Rodata status values recorded in Meta.Rodata.Status.
const (
	RodataOK          = "ok"
	RodataUnavailable = "unavailable"
	RodataDisabled    = "disabled"
	RodataTruncated   = "truncated"
)

// Meta is the meta.json member. The go_version/goarch/ptr_size/
// big_endian fields are convenience copies for humans inspecting a
// bundle; the heap dump's own Params record remains the authoritative
// source for runtime layout lookup during analysis.
type Meta struct {
	FormatVersion int    `json:"format_version"`
	CreatedAt     string `json:"created_at"` // RFC 3339, UTC
	Producer      string `json:"producer"`

	GoVersion string `json:"go_version"`
	GOARCH    string `json:"goarch"`
	PtrSize   int    `json:"ptr_size"`
	BigEndian bool   `json:"big_endian"`

	Rodata   RodataMeta `json:"rodata"`
	Warnings []string   `json:"warnings"`
}

// RodataMeta describes the read-only segment snapshot carried by the
// bundle.
type RodataMeta struct {
	// Status is one of the Rodata* constants.
	Status string `json:"status"`
	// Reason is human-readable context when Status is not "ok".
	Reason string `json:"reason,omitempty"`
	// Segments is the number of rodata/NNNNN.bin members.
	Segments int `json:"segments"`
	// TotalBytes is the sum of segment sizes.
	TotalBytes uint64 `json:"total_bytes"`
}

// SegmentInfo is one entry of the rodata/segments.json member,
// index-aligned with the rodata/NNNNN.bin members.
type SegmentInfo struct {
	// Member is the tar member holding the segment bytes.
	Member string `json:"member"`
	// Addr is the runtime virtual address of the first byte.
	Addr HexUint64 `json:"addr"`
	// Size is the segment length in bytes (always < 2^53, safe as a
	// plain JSON number).
	Size uint64 `json:"size"`
	// Perms is the mapping permission string, e.g. "r--" or "r-x".
	Perms string `json:"perms"`
	// Path is the backing file of the mapping, when known.
	Path string `json:"path,omitempty"`
}

// HexUint64 marshals as a "0x..."-prefixed JSON string: virtual
// addresses routinely exceed JSON's exact float53 integer range.
type HexUint64 uint64

// MarshalJSON implements json.Marshaler.
func (h HexUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal("0x" + strconv.FormatUint(uint64(h), 16))
}

// UnmarshalJSON implements json.Unmarshaler.
func (h *HexUint64) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bundle: address must be a hex string: %w", err)
	}
	hexDigits, ok := strings.CutPrefix(s, "0x")
	if !ok {
		return fmt.Errorf("bundle: address %q lacks 0x prefix", s)
	}
	v, err := strconv.ParseUint(hexDigits, 16, 64)
	if err != nil {
		return fmt.Errorf("bundle: parse address %q: %w", s, err)
	}
	*h = HexUint64(v)
	return nil
}
