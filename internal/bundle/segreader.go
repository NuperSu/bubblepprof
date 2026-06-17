package bundle

import (
	"fmt"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
)

// memorySegment is one saved read-only segment held in memory.
type memorySegment struct {
	start uint64
	end   uint64
	data  []byte
}

// SegmentsReader implements addrspace.Reader over the read-only
// segments saved in a bundle, keyed by their original runtime virtual
// addresses. It mirrors the ProcessReader contract: a read must lie
// entirely within a single segment; size==0 succeeds; addr==0 and
// overflowing ranges fail.
type SegmentsReader struct {
	segments []memorySegment // sorted by start
}

var _ addrspace.NamedReader = (*SegmentsReader)(nil)

// NewSegmentsReader builds a SegmentsReader. Each data slice i covers
// [infos[i].Addr, infos[i].Addr+len(data[i])); infos and data must be
// index-aligned with len(data[i]) == infos[i].Size.
func NewSegmentsReader(infos []SegmentInfo, data [][]byte) (*SegmentsReader, error) {
	if len(infos) != len(data) {
		return nil, fmt.Errorf("bundle: %d segment infos but %d data blocks", len(infos), len(data))
	}
	segs := make([]memorySegment, 0, len(infos))
	for i, info := range infos {
		if uint64(len(data[i])) != info.Size {
			return nil, fmt.Errorf("bundle: segment %s has %d bytes, expected %d", info.Member, len(data[i]), info.Size)
		}
		end, ok := addrspace.AddUint64(uint64(info.Addr), info.Size)
		if !ok {
			return nil, fmt.Errorf("bundle: segment %s range overflows", info.Member)
		}
		if info.Size == 0 {
			continue
		}
		segs = append(segs, memorySegment{start: uint64(info.Addr), end: end, data: data[i]})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].start < segs[j].start })
	for i := 1; i < len(segs); i++ {
		if segs[i].start < segs[i-1].end {
			return nil, fmt.Errorf(
				"bundle: segment ranges [0x%x,0x%x) and [0x%x,0x%x) overlap",
				segs[i-1].start, segs[i-1].end, segs[i].start, segs[i].end,
			)
		}
	}
	return &SegmentsReader{segments: segs}, nil
}

// Name implements addrspace.NamedReader.
func (r *SegmentsReader) Name() string { return "bundle-rodata" }

// ReadAtAddr implements addrspace.Reader.
func (r *SegmentsReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end, ok := addrspace.AddUint64(addr, size)
	if !ok {
		return nil, false
	}
	// First segment with start > addr; the candidate is the one before.
	i := sort.Search(len(r.segments), func(i int) bool {
		return r.segments[i].start > addr
	})
	if i == 0 {
		return nil, false
	}
	s := r.segments[i-1]
	if addr < s.start || end > s.end {
		return nil, false
	}
	out := make([]byte, size)
	copy(out, s.data[addr-s.start:end-s.start])
	return out, true
}
