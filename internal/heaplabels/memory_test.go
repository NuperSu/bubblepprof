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
