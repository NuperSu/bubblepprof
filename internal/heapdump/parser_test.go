package heapdump

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestParseHeaderRejectsUnknown(t *testing.T) {
	_, err := Parse(strings.NewReader("not a heap dump\n"), Options{})
	if err == nil {
		t.Fatal("expected error for bad header")
	}
	if !strings.Contains(err.Error(), "unsupported heap dump header") {
		t.Fatalf("got %v", err)
	}
}

func TestParseEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		PtrSize:      8,
		GOARCH:       "amd64",
		BuildVersion: "go-test",
		NumCPU:       4,
	})
	writeUvarint(&buf, tagEOF)

	snap, err := Parse(&buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Header != Header {
		t.Fatalf("header = %q", snap.Header)
	}
	if snap.Params.PtrSize != 8 {
		t.Fatalf("ptr size = %d", snap.Params.PtrSize)
	}
	if snap.Params.GOARCH != "amd64" {
		t.Fatalf("goarch = %q", snap.Params.GOARCH)
	}
	if snap.Stats.ObjectCount != 0 || snap.Stats.GoroutineCount != 0 {
		t.Fatalf("expected empty stats, got %+v", snap.Stats)
	}
}

func TestParseObjectExtractsPointers(t *testing.T) {
	buf := newSyntheticBuffer()

	contents := make([]byte, 24)
	binary.LittleEndian.PutUint64(contents[0:8], 0xdeadbeef)
	binary.LittleEndian.PutUint64(contents[16:24], 0x1234)

	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
		{Kind: heapsnapshot.FieldKindPtr, Offset: 16},
	})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(snap.Objects))
	}
	obj := snap.Objects[0]
	if obj.Addr != 0x1000 {
		t.Fatalf("addr = 0x%x", obj.Addr)
	}
	if obj.Size != 24 {
		t.Fatalf("size = %d", obj.Size)
	}
	want := []uint64{0xdeadbeef, 0x1234}
	if !equalUint64(obj.PointerAddrs, want) {
		t.Fatalf("pointers = %v, want %v", obj.PointerAddrs, want)
	}
	// Contents should be dropped by default.
	if obj.Contents != nil {
		t.Fatalf("contents kept unexpectedly: %v", obj.Contents)
	}
}

func TestParseObjectKeepsContentsOptionally(t *testing.T) {
	buf := newSyntheticBuffer()
	contents := []byte("payload!")
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x2000)
	writeBytes(buf, contents)
	writeFieldList(buf, nil)
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{KeepObjectContents: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := snap.Objects[0].Contents; string(got) != "payload!" {
		t.Fatalf("contents = %q", got)
	}
}

func TestParseObjectZeroPointerSkipped(t *testing.T) {
	buf := newSyntheticBuffer()

	contents := make([]byte, 8) // zero pointer
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x3000)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Objects[0].PointerAddrs) != 0 {
		t.Fatalf("expected no decoded pointers, got %v", snap.Objects[0].PointerAddrs)
	}
}

func TestParseObjectInterfaceFieldsPreservedButNotDecoded(t *testing.T) {
	buf := newSyntheticBuffer()

	contents := make([]byte, 16)
	binary.LittleEndian.PutUint64(contents[8:16], 0x4000)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x3500)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindEface, Offset: 0}})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(snap.Objects))
	}
	obj := snap.Objects[0]
	if len(obj.Fields) != 1 || obj.Fields[0].Kind != heapsnapshot.FieldKindEface {
		t.Fatalf("fields = %+v", obj.Fields)
	}
	if len(obj.PointerAddrs) != 0 {
		t.Fatalf("expected iface/eface pointer not to be decoded, got %v", obj.PointerAddrs)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", snap.Warnings)
	}
}

func TestParseGoroutineAndStackFrames(t *testing.T) {
	buf := newSyntheticBuffer()

	// Goroutine record.
	writeUvarint(buf, tagGoroutine)
	writeUvarint(buf, 0xaa00)  // addr
	writeUvarint(buf, 0xbb00)  // sp
	writeUvarint(buf, 42)      // goid
	writeUvarint(buf, 0xcc00)  // gopc
	writeUvarint(buf, 4)       // status
	writeBool(buf, false)      // isSystem
	writeBool(buf, false)      // isBackground
	writeUvarint(buf, 0)       // wait since
	writeString(buf, "select") // wait reason
	writeUvarint(buf, 0)       // ctxt
	writeUvarint(buf, 0)       // m
	writeUvarint(buf, 0)       // defer
	writeUvarint(buf, 0)       // panic

	// Stack frame with one pointer at offset 0 -> 0x3000.
	frameContents := make([]byte, 16)
	binary.LittleEndian.PutUint64(frameContents[0:8], 0x3000)
	writeUvarint(buf, tagStackFrame)
	writeUvarint(buf, 0xbb00)      // sp
	writeUvarint(buf, 0)           // depth
	writeUvarint(buf, 0)           // childSP
	writeBytes(buf, frameContents) // contents
	writeUvarint(buf, 0xff00)      // entry pc
	writeUvarint(buf, 0xff10)      // current pc
	writeUvarint(buf, 0xff20)      // cont pc
	writeString(buf, "main.run")   // func name
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Goroutines) != 1 {
		t.Fatalf("expected 1 goroutine, got %d", len(snap.Goroutines))
	}
	g := snap.Goroutines[0]
	if g.ID != 42 {
		t.Fatalf("id = %d", g.ID)
	}
	if g.WaitReason != "select" {
		t.Fatalf("wait reason = %q", g.WaitReason)
	}
	if len(g.Frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(g.Frames))
	}
	frame := g.Frames[0]
	if frame.FuncName != "main.run" {
		t.Fatalf("func name = %q", frame.FuncName)
	}
	if got, want := frame.PointerAddrs, []uint64{0x3000}; !equalUint64(got, want) {
		t.Fatalf("pointers = %v, want %v", got, want)
	}
}

// Stack frame pointer slot addresses must be recorded as sp + offset so
// downstream tooling can attribute roots to specific stack locations.
func TestParseStackFrameRecordsPointerSlots(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagGoroutine)
	writeUvarint(buf, 0xaa00) // addr
	writeUvarint(buf, 0xbb00) // sp
	writeUvarint(buf, 1)      // goid
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeBool(buf, false)
	writeBool(buf, false)
	writeUvarint(buf, 0)
	writeString(buf, "")
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)

	frameContents := make([]byte, 24)
	binary.LittleEndian.PutUint64(frameContents[0:8], 0x3000)
	binary.LittleEndian.PutUint64(frameContents[16:24], 0x4000)
	writeUvarint(buf, tagStackFrame)
	writeUvarint(buf, 0xbb00) // sp — anchor for slot addresses
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeBytes(buf, frameContents)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeString(buf, "main.f")
	writeFieldList(buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
		{Kind: heapsnapshot.FieldKindPtr, Offset: 16},
	})
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	frame := snap.Goroutines[0].Frames[0]
	if got, want := frame.PointerAddrs, []uint64{0x3000, 0x4000}; !equalUint64(got, want) {
		t.Fatalf("PointerAddrs = %v, want %v", got, want)
	}
	if got, want := frame.PointerSlots, []uint64{0xbb00, 0xbb10}; !equalUint64(got, want) {
		t.Fatalf("PointerSlots = %v, want %v (sp + offset)", got, want)
	}
}

func TestParseDataAndBSSGlobals(t *testing.T) {
	buf := newSyntheticBuffer()

	dataContents := make([]byte, 16)
	binary.LittleEndian.PutUint64(dataContents[0:8], 0x9100)
	binary.LittleEndian.PutUint64(dataContents[8:16], 0x9200)

	writeUvarint(buf, tagData)
	writeUvarint(buf, 0xd000)
	writeBytes(buf, dataContents)
	writeFieldList(buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
		{Kind: heapsnapshot.FieldKindPtr, Offset: 8},
	})

	writeUvarint(buf, tagBSS)
	writeUvarint(buf, 0xe000)
	writeBytes(buf, dataContents)
	writeFieldList(buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
	})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Data) != 1 || len(snap.BSS) != 1 {
		t.Fatalf("expected 1 data + 1 bss, got %d + %d", len(snap.Data), len(snap.BSS))
	}
	if len(snap.Globals) != 3 {
		t.Fatalf("expected 3 global roots, got %d", len(snap.Globals))
	}
	// First data root must record the slot address (segmentAddr+offset).
	if snap.Globals[0].Kind != "data" || snap.Globals[0].Addr != 0xd000 || snap.Globals[0].PointerAddr != 0x9100 {
		t.Fatalf("data root = %+v", snap.Globals[0])
	}
	if snap.Globals[2].Kind != "bss" || snap.Globals[2].Addr != 0xe000 {
		t.Fatalf("bss root = %+v", snap.Globals[2])
	}
}

func TestParseDataGlobalsUseSlotForNonZeroPointer(t *testing.T) {
	buf := newSyntheticBuffer()

	dataContents := make([]byte, 16)
	binary.LittleEndian.PutUint64(dataContents[8:16], 0x9200)

	writeUvarint(buf, tagData)
	writeUvarint(buf, 0xd000)
	writeBytes(buf, dataContents)
	writeFieldList(buf, []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 0},
		{Kind: heapsnapshot.FieldKindPtr, Offset: 8},
	})

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Globals) != 1 {
		t.Fatalf("expected 1 global root, got %d", len(snap.Globals))
	}
	root := snap.Globals[0]
	if root.Kind != "data" || root.Addr != 0xd008 || root.PointerAddr != 0x9200 {
		t.Fatalf("data root = %+v", root)
	}
}

func TestParseOtherRoot(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagOtherRoot)
	writeString(buf, "scheduler")
	writeUvarint(buf, 0xabcd)
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Globals) != 1 {
		t.Fatalf("expected 1 root, got %d", len(snap.Globals))
	}
	if snap.Globals[0].Kind != "otherroot" || snap.Globals[0].Description != "scheduler" || snap.Globals[0].PointerAddr != 0xabcd {
		t.Fatalf("root = %+v", snap.Globals[0])
	}
}

func TestParseTypeAndItab(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagType)
	writeUvarint(buf, 0x4000)
	writeUvarint(buf, 32)
	writeString(buf, "pkg.MyType")
	writeBool(buf, true)

	writeUvarint(buf, tagItab)
	writeUvarint(buf, 0x5000)
	writeUvarint(buf, 0x4000)

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ti, ok := snap.Types[0x4000]
	if !ok || ti.Name != "pkg.MyType" || ti.Size != 32 || !ti.IndirectIface {
		t.Fatalf("type = %+v", ti)
	}
	it, ok := snap.Itabs[0x5000]
	if !ok || it.TypeAddr != 0x4000 {
		t.Fatalf("itab = %+v", it)
	}
}

// In strict mode an unknown tag is a hard error.
func TestParseUnknownTagStrictFails(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, 99) // unknown
	_, err := Parse(buf, Options{Strict: true})
	if err == nil || !strings.Contains(err.Error(), "unknown record tag") {
		t.Fatalf("got err=%v, want unknown record tag", err)
	}
}

// In non-strict mode an unknown tag stops parsing but returns the partial
// snapshot with a warning and an UnknownRecords counter increment. This
// keeps the tool usable when a future Go runtime adds a new heap-dump
// record type.
func TestParseUnknownTagNonStrictWarns(t *testing.T) {
	buf := newSyntheticBuffer()

	// One real object, then an unknown tag, then content the parser will
	// never reach.
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, make([]byte, 8))
	writeFieldList(buf, nil)

	writeUvarint(buf, 99) // unknown
	writeUvarint(buf, 0xdead)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("non-strict parse should not error on unknown tag: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected the pre-unknown-tag object to be preserved, got %d objects", len(snap.Objects))
	}
	if snap.Stats.UnknownRecords != 1 {
		t.Fatalf("UnknownRecords = %d, want 1", snap.Stats.UnknownRecords)
	}
	found := false
	for _, w := range snap.Warnings {
		if strings.Contains(w, "unknown record tag 99") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unknown-tag warning, got %v", snap.Warnings)
	}
}

func TestParseOutOfBoundsPointerWarns(t *testing.T) {
	buf := newSyntheticBuffer()
	contents := make([]byte, 4) // too small to hold an 8-byte pointer
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x6000)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Warnings) == 0 {
		t.Fatalf("expected at least one warning, got none")
	}
}

func TestParsePointerOffsetOverflowWarns(t *testing.T) {
	buf := newSyntheticBuffer()
	contents := make([]byte, 8)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x7000)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: ^uint64(0)}})
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Warnings) == 0 {
		t.Fatalf("expected at least one warning, got none")
	}
}

func TestParseOutOfBoundsPointerStrictErrors(t *testing.T) {
	buf := newSyntheticBuffer()
	contents := make([]byte, 4)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x6000)
	writeBytes(buf, contents)
	writeFieldList(buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})
	writeUvarint(buf, tagEOF)

	_, err := Parse(buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected strict mode to fail on out-of-bounds pointer")
	}
}

func TestParseMaxMemRangeLimit(t *testing.T) {
	build := func() *bytes.Buffer {
		buf := newSyntheticBuffer()
		writeUvarint(buf, tagObject)
		writeUvarint(buf, 0x8000)
		writeBytes(buf, make([]byte, 8))
		writeFieldList(buf, nil)
		writeUvarint(buf, tagEOF)
		return buf
	}

	_, err := Parse(build(), Options{MaxMemRangeBytes: 4})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("got err=%v, want exceeds limit", err)
	}

	if _, err := Parse(build(), Options{MaxMemRangeBytes: 0}); err != nil {
		t.Fatalf("parse with zero limit: %v", err)
	}
}

func equalUint64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
