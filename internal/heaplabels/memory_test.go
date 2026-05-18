package heaplabels

import (
	"encoding/binary"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestMemoryRead(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Contents: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		},
	}
	mem := NewMemory(snap)

	if got, ok := mem.Read(0x1000, 8); !ok || len(got) != 8 || got[0] != 1 || got[7] != 8 {
		t.Fatalf("Read exact = %v, %t", got, ok)
	}
	if got, ok := mem.Read(0x1002, 3); !ok || string(got) != string([]byte{3, 4, 5}) {
		t.Fatalf("Read interior = %v, %t", got, ok)
	}
	if _, ok := mem.Read(0x1006, 4); ok {
		t.Fatalf("Read crossing boundary succeeded")
	}
	if _, ok := mem.Read(0, 1); ok {
		t.Fatalf("Read zero address succeeded")
	}
}

func TestMemory_Name(t *testing.T) {
	mem := NewMemory(nil)
	if got := mem.Name(); got != "heap" {
		t.Fatalf("Name() = %q, want %q", got, "heap")
	}
}

func TestMemory_Read_Overflow(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte{1, 2, 3, 4}}},
	})
	// addr + size overflows uint64
	if _, ok := mem.Read(^uint64(0), 8); ok {
		t.Fatal("overflow addr+size must fail")
	}
}

func TestMemory_ReadUintptr_MissingAddr(t *testing.T) {
	buf := make([]byte, 8)
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: buf}},
	})
	// 0xdead is not in memory
	if _, ok := mem.ReadUintptr(0xdead, 8, binary.LittleEndian); ok {
		t.Fatal("ReadUintptr with missing addr must fail")
	}
}

func TestMemory_ReadString_MissingAddr(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte("hello")}},
	})
	// 0xdead is not in memory
	if _, ok := mem.ReadString(0xdead, 5); ok {
		t.Fatal("ReadString with missing addr must fail")
	}
}

func TestNewMemory_SameStartAddr(t *testing.T) {
	// Two objects with the same Addr trigger the sort.Slice tie-breaker.
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Contents: []byte{1, 2, 3, 4, 5, 6, 7, 8}},         // [0x1000, 0x1008)
			{Addr: 0x1000, Contents: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}, // [0x1000, 0x100a)
		},
	}
	mem := NewMemory(snap)
	// Both ranges are stored; verify memory reads work (no panic from sort).
	if _, ok := mem.Read(0x1000, 8); !ok {
		t.Fatal("expected successful read from first range")
	}
}

func TestMemory_ReadString_ZeroLength(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte("hello")}},
	})
	s, ok := mem.ReadString(0x1000, 0)
	if !ok {
		t.Fatal("ReadString with length=0 must succeed")
	}
	if s != "" {
		t.Fatalf("ReadString with length=0 = %q, want empty string", s)
	}
}

func TestMemory_ReadString_Success(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte("hello")}},
	})
	s, ok := mem.ReadString(0x1000, 5)
	if !ok {
		t.Fatal("ReadString must succeed for valid addr+length in memory")
	}
	if s != "hello" {
		t.Fatalf("ReadString = %q, want %q", s, "hello")
	}
}

func TestMemoryReadUintptr(t *testing.T) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, 0x1122334455667788)
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: buf}},
	})

	got, ok := mem.ReadUintptr(0x1000, 8, binary.LittleEndian)
	if !ok || got != 0x1122334455667788 {
		t.Fatalf("ReadUintptr little = %#x, %t", got, ok)
	}
	got, ok = mem.ReadUintptr(0x1000, 4, binary.BigEndian)
	if !ok || got != 0x88776655 {
		t.Fatalf("ReadUintptr big = %#x, %t", got, ok)
	}
}
