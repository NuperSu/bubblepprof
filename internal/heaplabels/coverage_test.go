package heaplabels

import (
	"encoding/binary"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/runtimelayout"
)

func TestMemoryNilReceiver(t *testing.T) {
	var m *Memory
	if _, ok := m.Read(0x100, 4); ok {
		t.Fatal("Read on nil should fail")
	}
	if got, ok := (&Memory{}).Read(0x1, 0); !ok || len(got) != 0 {
		t.Fatalf("zero-size Read = %v, %t", got, ok)
	}
}

func TestMemoryReadUintptrUnsupportedPtrSize(t *testing.T) {
	buf := make([]byte, 8)
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: buf}},
	})
	if got, ok := mem.ReadUintptr(0x1000, 2, binary.LittleEndian); ok {
		t.Fatalf("ReadUintptr ptrSize=2 = %#x, %t", got, ok)
	}
}

func TestMemoryReadStringEmptyAndMissing(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte("hi!")}},
	})
	if got, ok := mem.ReadString(0xdead, 0); !ok || got != "" {
		t.Fatalf("zero length should always succeed: %q %t", got, ok)
	}
	if _, ok := mem.ReadString(0xdead, 3); ok {
		t.Fatal("ReadString on missing range should fail")
	}
}

func TestMemoryNewMemorySkipsEmptyContentsAndOverflow(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{
			{Addr: 0x100, Contents: nil},
			{Addr: ^uint64(0) - 1, Contents: []byte{1, 2, 3, 4}}, // addr+size overflows
			{Addr: 0x200, Contents: []byte{9}},
		},
	}
	mem := NewMemory(snap)
	if len(mem.ranges) != 1 {
		t.Fatalf("expected 1 range, got %d", len(mem.ranges))
	}
	if mem.ranges[0].Start != 0x200 {
		t.Fatalf("range start = %#x", mem.ranges[0].Start)
	}
}

func TestRangeContainingNilAndZeroAddr(t *testing.T) {
	var m *Memory
	if _, ok := m.rangeContaining(0x100); ok {
		t.Fatal("rangeContaining on nil should fail")
	}
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: []byte{1}}},
	})
	if _, ok := mem.rangeContaining(0); ok {
		t.Fatal("rangeContaining(0) should fail")
	}
	if _, ok := mem.rangeContaining(0x500); ok {
		t.Fatal("rangeContaining(missing) should fail")
	}
	if _, ok := mem.rangeContaining(0x1000); !ok {
		t.Fatal("rangeContaining(match) should succeed")
	}
}

func TestLookupLayoutMisses(t *testing.T) {
	cases := []*heapsnapshot.HeapSnapshot{
		nil,
		{Params: heapsnapshot.DumpParams{GOARCH: "arm64", PtrSize: 4, BuildVersion: "go1.26.0"}},
		{Params: heapsnapshot.DumpParams{GOARCH: "amd64", PtrSize: 4, BuildVersion: "go1.26.0"}},
		{Params: heapsnapshot.DumpParams{GOARCH: "amd64", PtrSize: 8, BuildVersion: "go9.99-future"}},
	}
	for i, snap := range cases {
		if _, ok := LookupLayout(snap); ok {
			t.Errorf("case %d: expected no match", i)
		}
	}
}

func TestStatusOfNonDecodeError(t *testing.T) {
	if got := statusOf(&otherErr{}); got != StatusMalformed {
		t.Fatalf("statusOf(non-decodeError) = %v", got)
	}
	if got := statusOf(decodeError{status: StatusStringMissing, msg: "x"}); got != StatusStringMissing {
		t.Fatalf("statusOf = %v", got)
	}
}

type otherErr struct{}

func (otherErr) Error() string { return "x" }

func TestDecodeAllAutoUnsupportedReportsEveryGoroutine(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 2, GOARCH: "amd64"},
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 1, Addr: 0x100},
		},
	}
	res := DecodeAllAuto(snap, Options{})
	if res.Stats.GoroutinesUnsupported != 1 {
		t.Fatalf("expected 1 unsupported, got stats %+v", res.Stats)
	}
}

func TestDecodeAllUnsupportedPtrSizeFromManualImpossible(t *testing.T) {
	// runtimelayout.Manual refuses non-8-byte pointers; passing such a
	// layout into DecodeAll is therefore only possible via a hand-crafted
	// Layout. Verify DecodeAll still surfaces unsupported_runtime rather
	// than panicking.
	snap := &heapsnapshot.HeapSnapshot{
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x100}},
	}
	layout := runtimelayout.Layout{PtrSize: 2}
	res := DecodeAll(snap, layout, Options{})
	if res.Stats.GoroutinesUnsupported != 1 {
		t.Fatalf("expected 1 unsupported, got %+v", res.Stats)
	}
	if res.Goroutines[0].Status != StatusUnsupportedRuntime {
		t.Fatalf("status = %s", res.Goroutines[0].Status)
	}
}

func TestDecodeAllAutoNoBuildVersionReturnsUnsupported(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 7, Addr: 0x500},
		},
	}
	res := DecodeAllAuto(snap, Options{})
	if res.Stats.GoroutinesUnsupported != 1 {
		t.Fatalf("expected 1 unsupported, got %+v", res.Stats)
	}
	if len(res.Goroutines) != 1 || res.Goroutines[0].Status != StatusUnsupportedRuntime {
		t.Fatalf("status = %+v", res.Goroutines)
	}
}

func TestDecodeAllAutoNilSnapshot(t *testing.T) {
	res := DecodeAllAuto(nil, Options{})
	if !strings.Contains(strings.Join(res.Warnings, "|"), "nil") {
		t.Fatalf("expected nil warning, got %v", res.Warnings)
	}
}

func TestDecodeAllNilSnapshot(t *testing.T) {
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(nil, layout, Options{})
	if !strings.Contains(strings.Join(res.Warnings, "|"), "nil") {
		t.Fatalf("expected nil warning, got %v", res.Warnings)
	}
}

func TestUnsupportedResultIncludesWarning(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x100}},
	}
	res := UnsupportedResult(snap, "unsupported test")
	if res.Stats.GoroutinesUnsupported != 1 || res.Goroutines[0].Status != StatusUnsupportedRuntime {
		t.Fatalf("unexpected: %+v", res)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "unsupported test" {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestDecodeLabelMapMalformedHeader(t *testing.T) {
	mapBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(mapBytes[0:8], 0x2000) // data ptr
	binary.LittleEndian.PutUint64(mapBytes[8:16], 5)     // len
	binary.LittleEndian.PutUint64(mapBytes[16:24], 2)    // cap < len

	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000) // labels ptr at offset 0x10

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{})
	if res.Stats.GoroutinesFailed != 1 {
		t.Fatalf("expected 1 failed, got %+v", res.Stats)
	}
	if res.Goroutines[0].Status != StatusMalformed {
		t.Fatalf("status = %v error = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
}

func TestDecodeLabelMapExceedsMaxLabels(t *testing.T) {
	mapBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(mapBytes[0:8], 0x2000)
	binary.LittleEndian.PutUint64(mapBytes[8:16], 5)
	binary.LittleEndian.PutUint64(mapBytes[16:24], 5)
	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{
		MaxLabels: 2,
	})
	if res.Goroutines[0].Status != StatusMalformed {
		t.Fatalf("expected malformed, got %v (%s)", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
}

func TestDecodeLabelMapEmptyOK(t *testing.T) {
	mapBytes := make([]byte, 24)
	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{})
	if res.Goroutines[0].Status != StatusDecoded {
		t.Fatalf("status = %v err = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
	if len(res.Goroutines[0].Labels) != 0 {
		t.Fatalf("labels = %v", res.Goroutines[0].Labels)
	}
}

func TestDecodeLabelMapNilDataPtrWithNonZeroLen(t *testing.T) {
	mapBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(mapBytes[0:8], 0)  // data ptr nil
	binary.LittleEndian.PutUint64(mapBytes[8:16], 2) // len > 0
	binary.LittleEndian.PutUint64(mapBytes[16:24], 2)
	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{})
	if res.Goroutines[0].Status != StatusLabelArrayMissing {
		t.Fatalf("status = %v err = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
}

func TestDecodeStringExceedsMaxLen(t *testing.T) {
	mapBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(mapBytes[0:8], 0x2000)
	binary.LittleEndian.PutUint64(mapBytes[8:16], 1)
	binary.LittleEndian.PutUint64(mapBytes[16:24], 1)
	labelArray := make([]byte, 32)
	binary.LittleEndian.PutUint64(labelArray[0:8], 0x3000)   // key data
	binary.LittleEndian.PutUint64(labelArray[8:16], 99999)   // key len huge
	binary.LittleEndian.PutUint64(labelArray[16:24], 0x4000) // val data
	binary.LittleEndian.PutUint64(labelArray[24:32], 0)      // val len 0

	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
			{Addr: 0x2000, Contents: labelArray},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{
		MaxStringLen: 32,
	})
	if res.Goroutines[0].Status != StatusMalformed {
		t.Fatalf("status = %v err = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
}

func TestDecodeStringMissingBytes(t *testing.T) {
	mapBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(mapBytes[0:8], 0x2000)
	binary.LittleEndian.PutUint64(mapBytes[8:16], 1)
	binary.LittleEndian.PutUint64(mapBytes[16:24], 1)
	labelArray := make([]byte, 32)
	binary.LittleEndian.PutUint64(labelArray[0:8], 0x3000)
	binary.LittleEndian.PutUint64(labelArray[8:16], 5)
	binary.LittleEndian.PutUint64(labelArray[16:24], 0x4000)
	binary.LittleEndian.PutUint64(labelArray[24:32], 0)

	gObj := make([]byte, 0x20)
	binary.LittleEndian.PutUint64(gObj[0x10:0x18], 0x1000)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
			{Addr: 0x1000, Contents: mapBytes},
			{Addr: 0x2000, Contents: labelArray},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{})
	if res.Goroutines[0].Status != StatusStringMissing {
		t.Fatalf("status = %v err = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
	}
	if res.Stats.StringsMissing != 1 {
		t.Fatalf("StringsMissing = %d", res.Stats.StringsMissing)
	}
}

func TestDecodeLabelsForGoroutineGAddrOverflow(t *testing.T) {
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
	})
	layout := mustManualLayout(t, ^uint64(0)) // huge offset
	g := heapsnapshot.Goroutine{ID: 1, Addr: 0x100}
	gr := DecodeLabelsForGoroutine(mem, mem, layout, Options{}, g)
	if gr.Status != StatusMalformed {
		t.Fatalf("status = %v error = %s", gr.Status, gr.Error)
	}
}

func TestDecodeLabelsForGoroutineNoLabels(t *testing.T) {
	gObj := make([]byte, 0x20)
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64", BuildVersion: "go1.26.3"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x500, Contents: gObj},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: 0x500}},
	}
	layout := mustManualLayout(t, 0x10)
	res := DecodeAll(snap, layout, Options{})
	if res.Goroutines[0].Status != StatusNoLabels {
		t.Fatalf("status = %v", res.Goroutines[0].Status)
	}
}

func TestContainsLabelsHelper(t *testing.T) {
	if !containsLabels(map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1"}) {
		t.Fatal("expected match")
	}
	if containsLabels(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
		t.Fatal("expected mismatch (value)")
	}
	if containsLabels(nil, map[string]string{"a": "1"}) {
		t.Fatal("expected mismatch (nil have)")
	}
}

func TestMulUint64Overflow(t *testing.T) {
	if _, ok := mulUint64(0, 5); !ok {
		t.Fatal("0 * 5 should be ok")
	}
	if _, ok := mulUint64(5, 0); !ok {
		t.Fatal("5 * 0 should be ok")
	}
	if _, ok := mulUint64(^uint64(0), 2); ok {
		t.Fatal("overflow should fail")
	}
	if got, ok := mulUint64(3, 4); !ok || got != 12 {
		t.Fatalf("3*4 = %d, %t", got, ok)
	}
}

func TestCopyLabelsNil(t *testing.T) {
	if got := copyLabels(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
