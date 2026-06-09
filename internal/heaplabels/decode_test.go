package heaplabels

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
)

func TestDecodeLabelMap(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}, {"job", "42"}})
	layout := mustManualLayout(t, 0x18)
	mem := NewMemory(snap)

	labels, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err != nil {
		t.Fatalf("DecodeLabelMap: %v", err)
	}
	if labels["bubble"] != "alpha" || labels["job"] != "42" {
		t.Fatalf("decoded labels = %#v", labels)
	}
}

func TestDecodeLabelMapDuplicateKeysLastWins(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "old"}, {"bubble", "new"}})
	layout := mustManualLayout(t, 0x18)
	mem := NewMemory(snap)

	labels, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err != nil {
		t.Fatalf("DecodeLabelMap: %v", err)
	}
	if labels["bubble"] != "new" {
		t.Fatalf("duplicate key result = %#v", labels)
	}
}

func TestDecodeLabelMapMalformedLenGreaterThanCap(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	mem := NewMemory(snap)
	layout := mustManualLayout(t, 0x18)
	writePtr(snap.Objects[1].Contents, 8, 2)
	writePtr(snap.Objects[1].Contents, 16, 1)

	_, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusMalformed {
		t.Fatalf("err = %v, want malformed", err)
	}
}

func TestDecodeLabelMapStringMissing(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	snap.Objects = snap.Objects[:3] // drop string objects
	layout := mustManualLayout(t, 0x18)
	mem := NewMemory(snap)

	_, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want string missing", err)
	}
}

func TestDecodeLabelsForGoroutine(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	layout := mustManualLayout(t, 0x18)
	got := DecodeAll(snap, layout, Options{})

	if got.Stats.GoroutinesDecoded != 1 {
		t.Fatalf("decoded goroutines = %d", got.Stats.GoroutinesDecoded)
	}
	if got.LabelsByGID[123]["bubble"] != "alpha" {
		t.Fatalf("labelsByGID = %#v", got.LabelsByGID)
	}
}

// TestDecodeAllMatchesVerifiedTable runs the production lookup-then-decode
// path (the same two steps /debug/memusage performs) against a synthetic
// snapshot whose g.labels offset matches the verified go1.26 amd64 entry.
func TestDecodeAllMatchesVerifiedTable(t *testing.T) {
	snap := syntheticLabelSnapshot(0x160, []kv{{"bubble", "alpha"}})
	snap.Params.BuildVersion = "go1.26.3-X:nodwarf5"

	layout, ok := runtimelayout.Lookup(LookupInputFromSnapshot(snap))
	if !ok {
		t.Fatal("verified table should match go1.26.* amd64")
	}
	got := DecodeAll(snap, layout, Options{})
	if got.Stats.GoroutinesDecoded != 1 {
		t.Fatalf("decoded = %d, stats=%+v", got.Stats.GoroutinesDecoded, got.Stats)
	}
	if got.LabelsByGID[123]["bubble"] != "alpha" {
		t.Fatalf("labelsByGID = %#v", got.LabelsByGID)
	}
}

func TestDecodeLabelsNoLabels(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	writePtr(snap.Objects[0].Contents, 0x18, 0)
	layout := mustManualLayout(t, 0x18)
	got := DecodeAll(snap, layout, Options{})
	if got.Stats.GoroutinesNoLabels != 1 || got.Goroutines[0].Status != StatusNoLabels {
		t.Fatalf("result = %#v", got)
	}
}

func TestFindOffsetCandidates(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}, {"job", "42"}})
	candidates := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if len(candidates) != 1 || candidates[0].Offset != 0x20 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestFindOffsetCandidatesAmbiguous(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}})
	writePtr(snap.Objects[0].Contents, 0x28, 0x1000)
	candidates := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if len(candidates) != 2 || candidates[0].Offset != 0x20 || candidates[1].Offset != 0x28 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

type kv struct {
	k string
	v string
}

func mustManualLayout(t *testing.T, gLabelsOffset uint64) runtimelayout.Layout {
	t.Helper()
	layout, err := runtimelayout.Manual(runtimelayout.LookupInput{
		GoVersion: "go1.test",
		GOARCH:    "amd64",
		PtrSize:   8,
		BigEndian: false,
	}, gLabelsOffset)
	if err != nil {
		t.Fatalf("runtimelayout.Manual: %v", err)
	}
	return layout
}

func syntheticLabelSnapshot(gLabelsOffset uint64, labels []kv) *heapsnapshot.HeapSnapshot {
	ptrSize := 8
	labelMap := make([]byte, 24)
	labelArray := make([]byte, len(labels)*32)
	writePtr(labelMap, 0, 0x2000)
	writePtr(labelMap, 8, uint64(len(labels)))
	writePtr(labelMap, 16, uint64(len(labels)))

	objects := []heapsnapshot.Object{
		{Addr: 0x5000, Contents: make([]byte, 0x200)},
		{Addr: 0x1000, Contents: labelMap},
		{Addr: 0x2000, Contents: labelArray},
	}
	writePtr(objects[0].Contents, int(gLabelsOffset), 0x1000)

	nextString := uint64(0x3000)
	for i, p := range labels {
		labelOff := i * 32
		keyAddr := nextString
		nextString += 0x100
		valueAddr := nextString
		nextString += 0x100
		writeStringHeader(labelArray, labelOff, keyAddr, p.k)
		writeStringHeader(labelArray, labelOff+2*ptrSize, valueAddr, p.v)
		objects = append(objects,
			heapsnapshot.Object{Addr: keyAddr, Contents: []byte(p.k)},
			heapsnapshot.Object{Addr: valueAddr, Contents: []byte(p.v)},
		)
	}

	return &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			PtrSize: 8,
			GOARCH:  "amd64",
		},
		Objects: objects,
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 123, Addr: 0x5000},
		},
	}
}

// TestDecodeString_ZeroLength verifies that a string header with length==0
// returns an empty string without error.
func TestDecodeString_ZeroLength(t *testing.T) {
	layout := mustManualLayout(t, 0)
	headerBytes := make([]byte, 16)
	binary.LittleEndian.PutUint64(headerBytes[0:], 0x5000) // dataPtr (non-nil)
	binary.LittleEndian.PutUint64(headerBytes[8:], 0)      // length = 0

	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: headerBytes}},
	}
	mem := NewMemory(snap)
	s, err := DecodeString(mem, mem, layout, Options{}, 0x1000)
	if err != nil {
		t.Fatalf("DecodeString with length=0 returned error: %v", err)
	}
	if s != "" {
		t.Fatalf("DecodeString with length=0 = %q, want empty string", s)
	}
}

// TestDecodeString_HeaderDataPtrUnavailable verifies that when the string
// header itself is outside the structural reader, the dataPtr read fails and
// StatusStringMissing is returned.
func TestDecodeString_HeaderDataPtrUnavailable(t *testing.T) {
	layout := mustManualLayout(t, 0)
	// Empty memory — address 0x1000 is not in any object.
	mem := NewMemory(&heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
	})
	_, err := DecodeString(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want StatusStringMissing for unavailable header data pointer", err)
	}
}

// TestDecodeString_HeaderLengthUnavailable verifies that when only the first
// pointer-sized word of the string header is readable (dataPtr), but the
// length field is outside the structural reader, StatusStringMissing is returned.
func TestDecodeString_HeaderLengthUnavailable(t *testing.T) {
	layout := mustManualLayout(t, 0)
	// Object has exactly 8 bytes: covers dataPtr read but not the length field.
	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: make([]byte, 8)}},
	}
	mem := NewMemory(snap)
	_, err := DecodeString(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want StatusStringMissing for unavailable length field", err)
	}
}

// TestDecodeString_NilDataPtr verifies the dataPtr==0 guard in DecodeString
// when the string header has a non-zero length but a nil data pointer.
func TestDecodeString_NilDataPtr(t *testing.T) {
	layout := mustManualLayout(t, 0)
	headerBytes := make([]byte, 16)
	binary.LittleEndian.PutUint64(headerBytes[0:], 0) // dataPtr = 0 (nil)
	binary.LittleEndian.PutUint64(headerBytes[8:], 1) // length = 1

	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: headerBytes}},
	}
	mem := NewMemory(snap)
	_, err := DecodeString(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want StatusStringMissing for nil data pointer", err)
	}
}

// TestDecodeString_LengthExceedsMax verifies that a string length exceeding
// Options.MaxStringLen returns StatusMalformed.
func TestDecodeString_LengthExceedsMax(t *testing.T) {
	layout := mustManualLayout(t, 0)
	headerBytes := make([]byte, 16)
	binary.LittleEndian.PutUint64(headerBytes[0:], 0x5000) // dataPtr (non-nil)
	binary.LittleEndian.PutUint64(headerBytes[8:], 100)    // length = 100

	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: headerBytes}},
	}
	mem := NewMemory(snap)
	_, err := DecodeString(mem, mem, layout, Options{MaxStringLen: 10}, 0x1000)
	if err == nil || statusOf(err) != StatusMalformed {
		t.Fatalf("err = %v, want StatusMalformed for exceeded MaxStringLen", err)
	}
}

// TestDecodeLabelMap_LengthFieldUnavailable verifies that when only the first
// 8 bytes of the label map header are readable (dataPtr), the length read
// at offset+8 fails and StatusLabelsObjectMissing is returned.
func TestDecodeLabelMap_LengthFieldUnavailable(t *testing.T) {
	layout := mustManualLayout(t, 0)
	// 8-byte object: covers dataPtr but not length at offset 8.
	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: make([]byte, 8)}},
	}
	mem := NewMemory(snap)
	_, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusLabelsObjectMissing {
		t.Fatalf("err = %v, want StatusLabelsObjectMissing for unavailable length field", err)
	}
}

// TestDecodeLabelMap_CapacityFieldUnavailable verifies that when the 16-byte
// slice header has a readable length but capacity falls outside the object,
// StatusLabelsObjectMissing is returned.
func TestDecodeLabelMap_CapacityFieldUnavailable(t *testing.T) {
	layout := mustManualLayout(t, 0)
	// 16-byte object: covers dataPtr and length but not capacity at offset 16.
	// length must be non-zero so the early `length==0` return doesn't fire.
	objBytes := make([]byte, 16)
	binary.LittleEndian.PutUint64(objBytes[8:], 1) // length = 1
	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: objBytes}},
	}
	mem := NewMemory(snap)
	_, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusLabelsObjectMissing {
		t.Fatalf("err = %v, want StatusLabelsObjectMissing for unavailable capacity field", err)
	}
}

// TestDecodeLabelMap_ValueStringMissing verifies that when the key string
// decodes successfully but the value string body is not in the reader,
// the error propagates out of DecodeLabelMap.
func TestDecodeLabelMap_ValueStringMissing(t *testing.T) {
	layout := mustManualLayout(t, 0x18)

	labelMap := make([]byte, 24)
	labelArray := make([]byte, 32)
	writePtr(labelMap, 0, 0x2000)
	writePtr(labelMap, 8, 1)
	writePtr(labelMap, 16, 1)

	// Key body at 0x3000 (present in heap); value body at 0x9000 (absent).
	writeStringHeader(labelArray, 0, 0x3000, "k")
	writeStringHeader(labelArray, 16, 0x9000, "v")

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{
			{Addr: 0x1000, Contents: labelMap},
			{Addr: 0x2000, Contents: labelArray},
			{Addr: 0x3000, Contents: []byte("k")},
			// 0x9000 (value body) intentionally absent
		},
	}
	mem := NewMemory(snap)
	_, err := DecodeLabelMap(mem, mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want StatusStringMissing when value body is absent", err)
	}
}

// TestDecodeLabelMap_ArraySizeOverflow verifies the mulUint64 overflow guard:
// a length * LabelSize that overflows uint64 must return StatusMalformed.
func TestDecodeLabelMap_ArraySizeOverflow(t *testing.T) {
	layout := mustManualLayout(t, 0)

	// LabelSize == 32 for 64-bit LE. Pick length such that length*32 overflows.
	const hugeLen = uint64(1)<<59 + 1 // (1<<59+1)*32 > math.MaxUint64

	objBytes := make([]byte, 24)
	binary.LittleEndian.PutUint64(objBytes[0:], 0x5000)   // dataPtr (non-nil)
	binary.LittleEndian.PutUint64(objBytes[8:], hugeLen)  // length
	binary.LittleEndian.PutUint64(objBytes[16:], hugeLen) // capacity

	snap := &heapsnapshot.HeapSnapshot{
		Params:  heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{{Addr: 0x1000, Contents: objBytes}},
	}
	mem := NewMemory(snap)
	_, err := DecodeLabelMap(mem, mem, layout, Options{MaxLabels: math.MaxUint64}, 0x1000)
	if err == nil || statusOf(err) != StatusMalformed {
		t.Fatalf("err = %v, want StatusMalformed for array size overflow", err)
	}
}

// TestFindOffsetCandidates_BigEndianSnap verifies that a big-endian snapshot
// returns nil candidates (Manual rejects big-endian).
func TestFindOffsetCandidates_BigEndianSnap(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	snap.Params.BigEndian = true

	result := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if result != nil {
		t.Fatalf("expected nil for big-endian snap, got %v", result)
	}
}

// TestFindOffsetCandidates_EmptyWant verifies that an empty want map returns nil.
func TestFindOffsetCandidates_EmptyWant(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	result := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{}, Options{})
	if result != nil {
		t.Fatalf("expected nil for empty want map, got %v", result)
	}
}

// TestFindOffsetCandidates_NilMem verifies that passing nil mem causes a fresh
// Memory to be constructed internally.
func TestFindOffsetCandidates_NilMem(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}})
	result := FindOffsetCandidates(snap, nil, map[string]string{"bubble": "alpha"}, Options{})
	if len(result) == 0 {
		t.Fatal("expected at least one candidate when mem=nil (should be constructed internally)")
	}
}

// TestFindOffsetCandidates_GoroutineAddrNotInHeap verifies that a goroutine
// whose runtime.g address is not in any heap object is silently skipped.
func TestFindOffsetCandidates_GoroutineAddrNotInHeap(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}})
	// Add a goroutine whose Addr does not correspond to any object.
	snap.Goroutines = append(snap.Goroutines, heapsnapshot.Goroutine{ID: 999, Addr: 0xDEAD})
	result := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	for _, c := range result {
		for _, gid := range c.GoroutineIDs {
			if gid == 999 {
				t.Fatal("goroutine with missing heap object must be skipped")
			}
		}
	}
}

// TestFindOffsetCandidates_NonMatchingPtr exercises the path where a non-zero
// pointer at some goroutine offset doesn't decode to a matching label map,
// so the inner continue is taken.
func TestFindOffsetCandidates_NonMatchingPtr(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}})
	// Write a non-zero, non-label-map ptr at offset 0x10 to trigger decoding
	// failure (0x9000 is not in any heap object).
	writePtr(snap.Objects[0].Contents, 0x10, 0x9000)
	result := FindOffsetCandidates(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if len(result) != 1 || result[0].Offset != 0x20 {
		t.Fatalf("candidates = %v, want [0x20]", result)
	}
}

// plainReader implements addrspace.Reader without addrspace.NamedReader so
// asNamedReader wraps it in an unnamedReader, exercising Name() == "extra".
type plainReader struct{}

func (plainReader) ReadAtAddr(_, _ uint64) ([]byte, bool) { return nil, false }

func TestAsNamedReader_UnnamedReaderName(t *testing.T) {
	named := asNamedReader(plainReader{})
	if named == nil {
		t.Fatal("asNamedReader returned nil for non-nil reader")
	}
	if got := named.Name(); got != "extra" {
		t.Fatalf("unnamedReader.Name() = %q, want %q", got, "extra")
	}
}

func TestAsNamedReader_NilInput(t *testing.T) {
	if got := asNamedReader(nil); got != nil {
		t.Fatalf("asNamedReader(nil) = %v, want nil", got)
	}
}

func writeStringHeader(buf []byte, off int, addr uint64, s string) {
	writePtr(buf, off, addr)
	writePtr(buf, off+8, uint64(len(s)))
}

func writePtr(buf []byte, off int, value uint64) {
	binary.LittleEndian.PutUint64(buf[off:off+8], value)
}
