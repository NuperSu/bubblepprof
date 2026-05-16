//go:build linux

package addrspace

import (
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

func TestProcessReader_RejectsWritableMappings(t *testing.T) {
	// Construct a ProcessReader with only writable mappings to verify
	// that ReadAtAddr rejects them all regardless of address.
	r := &ProcessReader{
		maps: []Mapping{
			{Start: 0x1000, End: 0x2000, Read: true, Write: true},  // rw- (heap-like)
			{Start: 0x3000, End: 0x4000, Read: true, Write: true, Exec: true}, // rwx
		},
		mem: nil, // closed/nil — any successful lookup would panic on read
	}
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("must reject rw- mapping (heap/stack)")
	}
	if _, ok := r.ReadAtAddr(0x3000, 8); ok {
		t.Fatal("must reject rwx mapping")
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

// stringHeader mirrors reflect.StringHeader without depending on its
// (deprecated) public type.
type stringHeader struct {
	Data uintptr
	Len  int
}
