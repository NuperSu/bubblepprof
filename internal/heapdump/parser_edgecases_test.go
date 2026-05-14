package heapdump

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestParseObjectBigEndianPointers(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		BigEndian:    true,
		PtrSize:      8,
		GOARCH:       "s390x",
		BuildVersion: "go-test",
		NumCPU:       2,
	})

	contents := make([]byte, 8)
	binary.BigEndian.PutUint64(contents, 0x0102030405060708)
	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x1000)
	writeBytes(&buf, contents)
	writeFieldList(&buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})
	writeUvarint(&buf, tagEOF)

	snap, err := Parse(&buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := snap.Objects[0].PointerAddrs, []uint64{0x0102030405060708}; !equalUint64(got, want) {
		t.Fatalf("PointerAddrs = %x, want %x", got, want)
	}
}

func TestParseObject32BitPointers(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		PtrSize:      4,
		GOARCH:       "386",
		BuildVersion: "go-test",
		NumCPU:       1,
	})

	contents := make([]byte, 8)
	binary.LittleEndian.PutUint32(contents[0:4], 0x12345678)
	binary.LittleEndian.PutUint32(contents[4:8], 0x90abcdef)
	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x2000)
	writeBytes(&buf, contents)
	writeFieldList(&buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
		{Kind: heapsnapshot.FieldKindPtr, Offset: 4},
	})
	writeUvarint(&buf, tagEOF)

	snap, err := Parse(&buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []uint64{0x12345678, 0x90abcdef}
	if got := snap.Objects[0].PointerAddrs; !equalUint64(got, want) {
		t.Fatalf("PointerAddrs = %x, want %x", got, want)
	}
}

func TestParseFinalizerAndQueuedFinalizerRecords(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagFinalizer)
	writeUvarint(buf, 0x1000) // object
	writeUvarint(buf, 0x2000) // fn val
	writeUvarint(buf, 0x3000) // fn pc
	writeUvarint(buf, 0x4000) // fint/type
	writeUvarint(buf, 0x5000) // ptr type

	writeUvarint(buf, tagQueuedFinalizer)
	writeUvarint(buf, 0x6000)
	writeUvarint(buf, 0x7000)
	writeUvarint(buf, 0x8000)
	writeUvarint(buf, 0x9000)
	writeUvarint(buf, 0xa000)

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(snap.Finalizers); got != 1 {
		t.Fatalf("Finalizers = %d", got)
	}
	if got := len(snap.QueuedFinalizers); got != 1 {
		t.Fatalf("QueuedFinalizers = %d", got)
	}
	if fin := snap.Finalizers[0]; fin.ObjectAddr != 0x1000 || fin.FuncVal != 0x2000 || fin.FuncPC != 0x3000 || fin.TypeAddr != 0x4000 || fin.PtrTypeAddr != 0x5000 {
		t.Fatalf("finalizer = %+v", fin)
	}
	if fin := snap.QueuedFinalizers[0]; fin.ObjectAddr != 0x6000 || fin.FuncVal != 0x7000 || fin.FuncPC != 0x8000 || fin.TypeAddr != 0x9000 || fin.PtrTypeAddr != 0xa000 {
		t.Fatalf("queued finalizer = %+v", fin)
	}
	if snap.Stats.FinalizerCount != 1 || snap.Stats.QueuedFinalizers != 1 {
		t.Fatalf("stats = %+v", snap.Stats)
	}
}

func TestParseUnsupportedPointerSizeRejectedInParams(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		PtrSize:      16,
		GOARCH:       "weird",
		BuildVersion: "go-test",
		NumCPU:       1,
	})

	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x1000)
	writeBytes(&buf, make([]byte, 16))
	writeFieldList(&buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})
	writeUvarint(&buf, tagEOF)

	_, err := Parse(&buf, Options{})
	if err == nil || !strings.Contains(err.Error(), "unsupported pointer size 16") {
		t.Fatalf("err = %v, want unsupported pointer-size error", err)
	}
}

func hasParseWarning(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}
