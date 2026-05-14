package heaplabels

import (
	"encoding/binary"

	"bubblepprof/internal/heapsnapshot"
)

const (
	DefaultMaxLabels    = 1024
	DefaultMaxStringLen = 1 << 20
)

type Layout struct {
	GOARCH  string
	PtrSize int
	Order   binary.ByteOrder

	GLabelsOffset uint64

	LabelMapSetOffset uint64
	SetListOffset     uint64

	SliceDataOffset uint64
	SliceLenOffset  uint64
	SliceCapOffset  uint64

	StringDataOffset uint64
	StringLenOffset  uint64

	LabelSize        uint64
	LabelKeyOffset   uint64
	LabelValueOffset uint64
}

type Options struct {
	GLabelsOffset    uint64
	HasGLabelsOffset bool

	MaxLabels    uint64
	MaxStringLen uint64
}

type LayoutEntry struct {
	VersionPrefix string
	GOARCH        string
	PtrSize       int
	GLabelsOffset uint64
}

var verifiedLayouts = []LayoutEntry{
	// Verified with cmd/labeloffsetprobe on linux/amd64:
	// go version go1.26.3-X:nodwarf5 linux/amd64.
	{
		VersionPrefix: "go1.26.",
		GOARCH:        "amd64",
		PtrSize:       8,
		GLabelsOffset: 0x160,
	},
}

func LookupGLabelsOffset(snap *heapsnapshot.HeapSnapshot) (uint64, bool) {
	if snap == nil {
		return 0, false
	}
	for _, e := range verifiedLayouts {
		if e.GOARCH != snap.Params.GOARCH || e.PtrSize != snap.Params.PtrSize {
			continue
		}
		if hasVersionPrefix(snap.Params.BuildVersion, e.VersionPrefix) {
			return e.GLabelsOffset, true
		}
	}
	return 0, false
}

func LayoutFromSnapshot(snap *heapsnapshot.HeapSnapshot, gLabelsOffset uint64) (Layout, bool) {
	if snap == nil {
		return Layout{}, false
	}
	ptrSize := snap.Params.PtrSize
	if ptrSize != 4 && ptrSize != 8 {
		return Layout{}, false
	}
	var order binary.ByteOrder = binary.LittleEndian
	if snap.Params.BigEndian {
		order = binary.BigEndian
	}
	ptr := uint64(ptrSize)
	return Layout{
		GOARCH:  snap.Params.GOARCH,
		PtrSize: ptrSize,
		Order:   order,

		GLabelsOffset: gLabelsOffset,

		LabelMapSetOffset: 0,
		SetListOffset:     0,

		SliceDataOffset: 0,
		SliceLenOffset:  ptr,
		SliceCapOffset:  2 * ptr,

		StringDataOffset: 0,
		StringLenOffset:  ptr,

		LabelSize:        4 * ptr,
		LabelKeyOffset:   0,
		LabelValueOffset: 2 * ptr,
	}, true
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

func hasVersionPrefix(version, prefix string) bool {
	if len(version) < len(prefix) {
		return false
	}
	return version[:len(prefix)] == prefix
}
