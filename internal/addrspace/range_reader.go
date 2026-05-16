package addrspace

import "sort"

// Range describes a contiguous mapping of bytes at virtual addresses
// [Start, End). End is exclusive: Start+len(Data)==End.
type Range struct {
	Start  uint64
	End    uint64
	Data   []byte
	Source string
}

// RangeReader serves reads from precomputed in-memory byte ranges. The
// heap-label decoder uses one of these to expose heap dump object
// contents through the addrspace.Reader interface.
type RangeReader struct {
	name   string
	ranges []Range
}

// NewRangeReader copies and sorts ranges by Start. Ranges with zero
// length, mismatched Start/End, or addr+size overflow are dropped.
func NewRangeReader(name string, ranges []Range) *RangeReader {
	sorted := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if r.End <= r.Start || len(r.Data) == 0 {
			continue
		}
		if uint64(len(r.Data)) != (r.End - r.Start) {
			continue
		}
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start == sorted[j].Start {
			return sorted[i].End < sorted[j].End
		}
		return sorted[i].Start < sorted[j].Start
	})
	return &RangeReader{name: name, ranges: sorted}
}

// ReadAtAddr implements Reader.
func (r *RangeReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end, ok := AddUint64(addr, size)
	if !ok {
		return nil, false
	}
	for _, rg := range r.ranges {
		if addr < rg.Start {
			return nil, false
		}
		if addr >= rg.Start && end <= rg.End {
			off := addr - rg.Start
			out := make([]byte, size)
			copy(out, rg.Data[off:off+size])
			return out, true
		}
	}
	return nil, false
}

// Name implements NamedReader. Returns the name supplied at
// construction or "range" if none was set.
func (r *RangeReader) Name() string {
	if r == nil || r.name == "" {
		return "range"
	}
	return r.name
}

// Ranges returns a copy of the indexed ranges, sorted by Start.
func (r *RangeReader) Ranges() []Range {
	if r == nil {
		return nil
	}
	out := make([]Range, len(r.ranges))
	copy(out, r.ranges)
	return out
}
