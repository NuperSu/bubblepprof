package addrspace

import "testing"

func TestRangeReaderReadAndBoundaries(t *testing.T) {
	r := NewRangeReader("heap", []Range{
		{Start: 0x1000, End: 0x1008, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{Start: 0x2000, End: 0x2004, Data: []byte{9, 9, 9, 9}},
	})
	got, ok := r.ReadAtAddr(0x1000, 8)
	if !ok || got[0] != 1 || got[7] != 8 {
		t.Fatalf("Read full range = %v ok=%t", got, ok)
	}
	got, ok = r.ReadAtAddr(0x1002, 3)
	if !ok || got[0] != 3 || got[2] != 5 {
		t.Fatalf("Read interior = %v ok=%t", got, ok)
	}
	if _, ok := r.ReadAtAddr(0x1006, 4); ok {
		t.Fatal("read crossing boundary must fail")
	}
	if _, ok := r.ReadAtAddr(0, 1); ok {
		t.Fatal("addr=0 with size>0 must fail")
	}
	if got, ok := r.ReadAtAddr(0, 0); !ok || len(got) != 0 {
		t.Fatalf("zero-size read = %v ok=%t", got, ok)
	}
	if _, ok := r.ReadAtAddr(0x3000, 1); ok {
		t.Fatal("missing range must fail")
	}
}

func TestRangeReaderDropsInvalidRanges(t *testing.T) {
	r := NewRangeReader("heap", []Range{
		{Start: 0x1000, End: 0x1000, Data: nil},                  // empty
		{Start: 0x2000, End: 0x2004, Data: nil},                  // nil data
		{Start: 0x3000, End: 0x3002, Data: []byte{1, 2, 3, 4}},   // size mismatch
		{Start: 0x4000, End: 0x4002, Data: []byte{8, 9}},         // valid
	})
	ranges := r.Ranges()
	if len(ranges) != 1 || ranges[0].Start != 0x4000 {
		t.Fatalf("expected 1 valid range starting at 0x4000, got %#v", ranges)
	}
}

func TestRangeReaderNilSafe(t *testing.T) {
	var r *RangeReader
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("nil receiver must fail")
	}
	if r.Name() != "range" {
		t.Fatalf("nil Name = %q", r.Name())
	}
	if r.Ranges() != nil {
		t.Fatal("nil Ranges must be nil")
	}
}

func TestRangeReaderReadAtAddr_Overflow(t *testing.T) {
	r := NewRangeReader("test", []Range{
		{Start: 0x1000, End: 0x1008, Data: make([]byte, 8)},
	})
	if _, ok := r.ReadAtAddr(^uint64(0), 8); ok {
		t.Fatal("overflow addr+size must fail")
	}
}

func TestRangeReaderSort_SameStart(t *testing.T) {
	// Two ranges with the same Start: sort falls back to End comparison.
	r := NewRangeReader("test", []Range{
		{Start: 0x1000, End: 0x1010, Data: make([]byte, 16)},
		{Start: 0x1000, End: 0x1008, Data: make([]byte, 8)},
	})
	ranges := r.Ranges()
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ranges, got %d", len(ranges))
	}
	// Shorter range should come first (End < End sorting)
	if ranges[0].End >= ranges[1].End {
		t.Fatalf("sort order: End[0]=%#x, End[1]=%#x — shorter should be first", ranges[0].End, ranges[1].End)
	}
}

func TestRangeReaderName(t *testing.T) {
	r := NewRangeReader("custom", nil)
	if r.Name() != "custom" {
		t.Fatalf("Name = %q", r.Name())
	}
	r = NewRangeReader("", nil)
	if r.Name() != "range" {
		t.Fatalf("empty Name fallback = %q", r.Name())
	}
}
