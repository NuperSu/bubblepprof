package addrspace

import (
	"encoding/binary"
	"testing"
)

// stubReader is a minimal NamedReader that returns canned bytes for one
// fixed [base, base+len(data)) range.
type stubReader struct {
	name string
	base uint64
	data []byte
}

func (s stubReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
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
	regionEnd := s.base + uint64(len(s.data))
	if addr < s.base || end > regionEnd {
		return nil, false
	}
	off := addr - s.base
	out := make([]byte, size)
	copy(out, s.data[off:off+size])
	return out, true
}

func (s stubReader) Name() string { return s.name }

func TestCompositeFirstReaderWins(t *testing.T) {
	a := stubReader{name: "a", base: 0x1000, data: []byte{0xAA, 0xAA, 0xAA, 0xAA}}
	b := stubReader{name: "b", base: 0x1000, data: []byte{0xBB, 0xBB, 0xBB, 0xBB}}
	c := Composite{Readers: []NamedReader{a, b}}
	got, ok := c.ReadAtAddr(0x1000, 4)
	if !ok || len(got) != 4 || got[0] != 0xAA {
		t.Fatalf("got %v ok=%t, want first reader bytes", got, ok)
	}
}

func TestCompositeFallsThrough(t *testing.T) {
	a := stubReader{name: "a", base: 0x1000, data: []byte{1, 2, 3, 4}}
	b := stubReader{name: "b", base: 0x2000, data: []byte{0xCC}}
	c := Composite{Readers: []NamedReader{a, b}}
	got, ok := c.ReadAtAddr(0x2000, 1)
	if !ok || got[0] != 0xCC {
		t.Fatalf("got %v ok=%t, want fall-through to b", got, ok)
	}
}

func TestCompositeAllFail(t *testing.T) {
	a := stubReader{name: "a", base: 0x1000, data: []byte{1}}
	c := Composite{Readers: []NamedReader{a}}
	if _, ok := c.ReadAtAddr(0xdead, 4); ok {
		t.Fatal("expected failure when no reader serves the range")
	}
}

func TestCompositeZeroSizeAlwaysOK(t *testing.T) {
	c := Composite{}
	if _, ok := c.ReadAtAddr(0xdead, 0); !ok {
		t.Fatal("zero-size read must succeed")
	}
	if _, ok := c.ReadAtAddr(0, 0); !ok {
		t.Fatal("zero-size read at zero address must succeed")
	}
}

func TestCompositeZeroAddressFailsForNonZeroSize(t *testing.T) {
	c := Composite{}
	if _, ok := c.ReadAtAddr(0, 1); ok {
		t.Fatal("read at addr=0 with size>0 must fail")
	}
}

func TestCompositeOverflowRejected(t *testing.T) {
	c := Composite{}
	if _, ok := c.ReadAtAddr(^uint64(0)-1, 8); ok {
		t.Fatal("addr+size overflow must fail")
	}
}

func TestMulUint64(t *testing.T) {
	if got, ok := MulUint64(0, 5); !ok || got != 0 {
		t.Fatalf("0*5 = %d, %t", got, ok)
	}
	if got, ok := MulUint64(5, 0); !ok || got != 0 {
		t.Fatalf("5*0 = %d, %t", got, ok)
	}
	if _, ok := MulUint64(^uint64(0), 2); ok {
		t.Fatal("overflow should fail")
	}
	if got, ok := MulUint64(3, 4); !ok || got != 12 {
		t.Fatalf("3*4 = %d, %t", got, ok)
	}
}

func TestCompositeSkipsNilReaders(t *testing.T) {
	b := stubReader{name: "b", base: 0x2000, data: []byte{0xCC}}
	c := Composite{Readers: []NamedReader{nil, b, nil}}
	got, ok := c.ReadAtAddr(0x2000, 1)
	if !ok || got[0] != 0xCC {
		t.Fatalf("got %v ok=%t", got, ok)
	}
}

func TestCompositeSourceFor(t *testing.T) {
	a := stubReader{name: "heap", base: 0x1000, data: []byte{1}}
	b := stubReader{name: "process", base: 0x2000, data: []byte{2}}
	c := Composite{Readers: []NamedReader{a, b}}

	src, ok := c.SourceFor(0x1000, 1)
	if !ok || src != "heap" {
		t.Fatalf("source for heap addr = %q, ok=%t", src, ok)
	}
	src, ok = c.SourceFor(0x2000, 1)
	if !ok || src != "process" {
		t.Fatalf("source for process addr = %q, ok=%t", src, ok)
	}
	if _, ok := c.SourceFor(0xdead, 1); ok {
		t.Fatal("missing addr should report no source")
	}
	if _, ok := c.SourceFor(0xdead, 0); !ok {
		t.Fatal("zero-size SourceFor must succeed")
	}
}

func TestCompositeName(t *testing.T) {
	a := stubReader{name: "heap"}
	b := stubReader{name: "process"}
	c := Composite{Readers: []NamedReader{a, b}}
	if got := c.Name(); got != "composite[heap,process]" {
		t.Fatalf("Name = %q", got)
	}
	empty := Composite{}
	if got := empty.Name(); got != "composite[]" {
		t.Fatalf("empty Name = %q", got)
	}
}

func TestReadString_ReadAtFails(t *testing.T) {
	r := stubReader{base: 0x1000, data: []byte("hello")}
	// addr 0xdead is not in the stub range → ReadAtAddr returns false → ReadString returns false
	if _, ok := ReadString(r, 0xdead, 5, 100); ok {
		t.Fatal("ReadString with missing addr must fail")
	}
}

func TestReadUintptr_ReadAtFails(t *testing.T) {
	r := stubReader{base: 0x1000, data: []byte("hello")}
	// addr 0xdead is not in the stub range → ReadAtAddr fails
	if _, ok := ReadUintptr(r, 0xdead, 8, binary.LittleEndian); ok {
		t.Fatal("ReadUintptr with missing addr must fail")
	}
}

func TestReadStringRespectsMax(t *testing.T) {
	r := stubReader{base: 0x1000, data: []byte("hello")}
	if got, ok := ReadString(r, 0x1000, 5, 100); !ok || got != "hello" {
		t.Fatalf("ReadString = %q ok=%t", got, ok)
	}
	if got, ok := ReadString(r, 0, 0, 100); !ok || got != "" {
		t.Fatalf("zero-length must succeed: %q ok=%t", got, ok)
	}
	if _, ok := ReadString(r, 0x1000, 999, 100); ok {
		t.Fatal("length > max must fail")
	}
	if got, ok := ReadString(r, 0x1000, 5, 0); !ok || got != "hello" {
		t.Fatalf("max=0 disables the limit: %q ok=%t", got, ok)
	}
}

func TestReadUintptr(t *testing.T) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, 0xdeadbeefcafebabe)
	r := stubReader{base: 0x1000, data: buf}
	if v, ok := ReadUintptr(r, 0x1000, 8, binary.LittleEndian); !ok || v != 0xdeadbeefcafebabe {
		t.Fatalf("ReadUintptr 8 = %#x ok=%t", v, ok)
	}
	if v, ok := ReadUintptr(r, 0x1000, 4, binary.LittleEndian); !ok || v != 0xcafebabe {
		t.Fatalf("ReadUintptr 4 = %#x ok=%t", v, ok)
	}
	if _, ok := ReadUintptr(r, 0x1000, 2, binary.LittleEndian); ok {
		t.Fatal("unsupported ptrSize must fail")
	}
	if _, ok := ReadUintptr(nil, 0x1000, 8, binary.LittleEndian); ok {
		t.Fatal("nil reader must fail")
	}
}

func TestCompositeName_WithNilReader(t *testing.T) {
	b := stubReader{name: "b"}
	c := Composite{Readers: []NamedReader{nil, b, nil}}
	if got := c.Name(); got != "composite[b]" {
		t.Fatalf("Name = %q, want composite[b]", got)
	}
}

func TestCompositeSourceFor_EdgeCases(t *testing.T) {
	b := stubReader{name: "b", base: 0x2000, data: []byte{1}}
	c := Composite{Readers: []NamedReader{nil, b}}

	// addr == 0
	if _, ok := c.SourceFor(0, 4); ok {
		t.Fatal("SourceFor addr=0 must fail")
	}
	// overflow
	if _, ok := c.SourceFor(^uint64(0), 4); ok {
		t.Fatal("SourceFor overflow must fail")
	}
	// nil reader in loop (covered by Readers: []NamedReader{nil, b})
	src, ok := c.SourceFor(0x2000, 1)
	if !ok || src != "b" {
		t.Fatalf("SourceFor with nil reader = %q ok=%t", src, ok)
	}
}

func TestReadString_NilReader(t *testing.T) {
	if _, ok := ReadString(nil, 0x1000, 5, 100); ok {
		t.Fatal("nil reader must fail")
	}
}

func TestReadUintptr_NilOrder(t *testing.T) {
	r := stubReader{base: 0x1000, data: make([]byte, 8)}
	if _, ok := ReadUintptr(r, 0x1000, 8, nil); ok {
		t.Fatal("nil order must fail")
	}
}

func TestAddUint64Overflow(t *testing.T) {
	if _, ok := AddUint64(^uint64(0), 1); ok {
		t.Fatal("overflow must be detected")
	}
	if got, ok := AddUint64(10, 20); !ok || got != 30 {
		t.Fatalf("normal add = %d ok=%t", got, ok)
	}
}
