//go:build linux

package addrspace

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func TestParseProcMaps(t *testing.T) {
	in := strings.NewReader("" +
		"55a000000000-55a000010000 r-xp 00000000 fd:01 1 /usr/bin/test\n" +
		"7f1234567000-7f1234568000 rw-p 00000000 00:00 0 \n" +
		"7fffaaaa0000-7fffaaaa1000 ---p 00000000 00:00 0 [stack]\n" +
		"garbage line\n" +
		"7fff00000000-7fff00100000 r--p 00000000 fd:00 2 [vdso]\n")
	got, err := parseProcMaps(in)
	if err != nil {
		t.Fatalf("parseProcMaps: %v", err)
	}
	want := []Mapping{
		{Start: 0x55a000000000, End: 0x55a000010000, Read: true, Exec: true, Path: "/usr/bin/test"},
		{Start: 0x7f1234567000, End: 0x7f1234568000, Read: true, Write: true, Path: ""},
		{Start: 0x7fffaaaa0000, End: 0x7fffaaaa1000, Path: "[stack]"},
		{Start: 0x7fff00000000, End: 0x7fff00100000, Read: true, Path: "[vdso]"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcMaps mismatch.\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProcessReader_RejectsHeapBackedString(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	if _, ok := r.ReadAtAddr(0, 8); ok {
		t.Fatal("read at addr=0 must fail")
	}

	// Force a heap allocation we can address by virtual address. The
	// string is built from a fresh []byte so the data pointer is
	// definitely heap-backed (Go heap is in a writable mapping).
	payload := []byte("process-reader-readback")
	s := string(payload)
	sh := (*stringHeader)(unsafe.Pointer(&s))
	if sh.Data == 0 || sh.Len == 0 {
		t.Fatalf("test string header invalid: %+v", sh)
	}

	// ProcessReader now rejects writable mappings (heap, stack, anonymous
	// RW). Heap strings must never be served — they are already covered by
	// the heap dump object contents and live in writable address space.
	if _, ok := r.ReadAtAddr(uint64(sh.Data), uint64(sh.Len)); ok {
		t.Fatalf("ProcessReader must reject heap-backed (writable) addresses at 0x%x len %d", sh.Data, sh.Len)
	}

	if got, ok := r.ReadAtAddr(0xdead, 0); !ok || len(got) != 0 {
		t.Fatalf("zero-size read = %v ok=%t", got, ok)
	}
}

func TestMappingEligibleForStringBody(t *testing.T) {
	cases := []struct {
		name string
		m    Mapping
		want bool
	}{
		{"r--p (rodata)", Mapping{Read: true}, true},
		{"r-xp (text)", Mapping{Read: true, Exec: true}, true},
		{"rw-p (heap/stack)", Mapping{Read: true, Write: true}, false},
		{"rwxp", Mapping{Read: true, Write: true, Exec: true}, false},
		{"---p (no perms)", Mapping{}, false},
		{"--xp (exec only)", Mapping{Exec: true}, false},
	}
	for _, tc := range cases {
		if got := mappingEligibleForStringBody(tc.m); got != tc.want {
			t.Errorf("%s: mappingEligibleForStringBody=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProcessReader_CloseIsIdempotent(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("read after Close should fail")
	}
}

func TestProcessReader_Name(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if got := r.Name(); got != "process" {
		t.Fatalf("Name() = %q, want %q", got, "process")
	}
}

func TestProcessReader_Mappings(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	m := r.Mappings()
	if len(m) == 0 {
		t.Fatal("Mappings() returned empty slice")
	}
}

func TestProcessReader_Mappings_NilReceiver(t *testing.T) {
	var r *ProcessReader
	if got := r.Mappings(); got != nil {
		t.Fatalf("(*ProcessReader)(nil).Mappings() = %v, want nil", got)
	}
}

func TestProcessReader_ReadAtAddr_NilReceiver(t *testing.T) {
	var r *ProcessReader
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("(*ProcessReader)(nil).ReadAtAddr should return false")
	}
}

func TestProcessReader_ReadAtAddr_Overflow(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	// addr + size overflows uint64 → should return false, not panic.
	const maxUint64 = ^uint64(0)
	if _, ok := r.ReadAtAddr(maxUint64, 8); ok {
		t.Fatal("overflow read should return false")
	}
}

func TestParseProcMaps_EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		count int
	}{
		{
			name:  "empty range end==start",
			input: "55a000000000-55a000000000 r-xp 00000000 fd:01 1\n",
			count: 0, // rejected: end <= start
		},
		{
			name:  "no dash in range",
			input: "55a000000000 r-xp 00000000 fd:01 1\n",
			count: 0,
		},
		{
			name:  "bad hex in start",
			input: "zzzz-55a000010000 r-xp 00000000 fd:01 1\n",
			count: 0,
		},
		{
			name:  "bad hex in end",
			input: "55a000000000-zzzz r-xp 00000000 fd:01 1\n",
			count: 0,
		},
		{
			name:  "perms string too short",
			input: "55a000000000-55a000010000 r- 00000000 fd:01 1\n",
			count: 0,
		},
		{
			name:  "only one field",
			input: "55a000000000-55a000010000\n",
			count: 0,
		},
		{
			name:  "empty line",
			input: "\n",
			count: 0,
		},
		{
			name:  "valid line with 5 fields (no path)",
			input: "55a000000000-55a000010000 r-xp 00000000 fd:01 1\n",
			count: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcMaps(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("parseProcMaps: %v", err)
			}
			if len(got) != tc.count {
				t.Fatalf("len = %d, want %d (got %+v)", len(got), tc.count, got)
			}
		})
	}
}

// TestProcessReader_ReadAtAddr_AddrBeyondMaxInt64 exercises the addr > math.MaxInt64
// guard inside ReadAtAddr. We inject a synthetic mapping with a virtual address in
// the high half of uint64 space so the loop enters the mapping-match branch and
// then hits the overflow guard before calling ReadAt.
func TestProcessReader_ReadAtAddr_AddrBeyondMaxInt64(t *testing.T) {
	mem, err := os.Open("/proc/self/mem")
	if err != nil {
		t.Skipf("open /proc/self/mem: %v", err)
	}
	defer mem.Close()

	const beyondMax = uint64(math.MaxInt64) + 1
	r := &ProcessReader{
		maps: []Mapping{{
			Start: beyondMax,
			End:   math.MaxUint64,
			Read:  true, // eligible (no Write flag)
		}},
		mem: mem,
	}
	if _, ok := r.ReadAtAddr(beyondMax, 8); ok {
		t.Fatal("ReadAtAddr with addr > MaxInt64 must return false")
	}
}

func TestProcessReader_EligibleStringRanges_Filters(t *testing.T) {
	r := &ProcessReader{maps: []Mapping{
		{Start: 0x1000, End: 0x2000, Read: true, Path: "/usr/bin/test"},              // eligible: r--
		{Start: 0x2000, End: 0x3000, Read: true, Exec: true, Path: "/usr/bin/test"},  // eligible: r-x
		{Start: 0x3000, End: 0x4000, Read: true, Write: true, Path: "/usr/bin/test"}, // writable: excluded
		{Start: 0x4000, End: 0x5000, Path: "[stack]"},                                // no read: excluded
	}}
	got := r.EligibleStringRanges()
	want := []Mapping{
		{Start: 0x1000, End: 0x2000, Read: true, Path: "/usr/bin/test"},
		{Start: 0x2000, End: 0x3000, Read: true, Exec: true, Path: "/usr/bin/test"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EligibleStringRanges mismatch.\n got: %#v\nwant: %#v", got, want)
	}

	var nilReader *ProcessReader
	if ranges := nilReader.EligibleStringRanges(); ranges != nil {
		t.Fatalf("nil receiver: got %#v, want nil", ranges)
	}
}

func TestProcessReader_EligibleStringRanges_CoversLiteral(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Skipf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	ranges := r.EligibleStringRanges()
	if len(ranges) == 0 {
		t.Fatal("EligibleStringRanges returned no ranges on a live process")
	}

	// A Go string literal's bytes live in a read-only segment, so some
	// returned range must cover it, and a read through ReadAtAddr from
	// inside that range must return the literal bytes.
	literal := "bubblepprof-eligible-ranges-probe"
	sh := (*stringHeader)(unsafe.Pointer(&literal))
	addr, size := uint64(sh.Data), uint64(sh.Len)
	covered := false
	for _, m := range ranges {
		if addr >= m.Start && addr+size <= m.End {
			covered = true
			break
		}
	}
	if !covered {
		t.Fatalf("no eligible range covers string literal at 0x%x", addr)
	}
	got, ok := r.ReadAtAddr(addr, size)
	if !ok || string(got) != literal {
		t.Fatalf("ReadAtAddr(literal) = %q ok=%t", got, ok)
	}
}

// stringHeader mirrors reflect.StringHeader without depending on its
// (deprecated) public type.
type stringHeader struct {
	Data uintptr
	Len  int
}
