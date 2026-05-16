package heaplabels

import (
	"encoding/binary"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestDecodeLabelMap(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}, {"job", "42"}})
	layout, ok := LayoutFromSnapshot(snap, 0x18)
	if !ok {
		t.Fatal("LayoutFromSnapshot failed")
	}

	labels, err := DecodeLabelMap(NewMemory(snap), layout, Options{}, 0x1000)
	if err != nil {
		t.Fatalf("DecodeLabelMap: %v", err)
	}
	if labels["bubble"] != "alpha" || labels["job"] != "42" {
		t.Fatalf("decoded labels = %#v", labels)
	}
}

func TestDecodeLabelMapDuplicateKeysLastWins(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "old"}, {"bubble", "new"}})
	layout, _ := LayoutFromSnapshot(snap, 0x18)

	labels, err := DecodeLabelMap(NewMemory(snap), layout, Options{}, 0x1000)
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
	layout, _ := LayoutFromSnapshot(snap, 0x18)
	writePtr(snap.Objects[1].Contents, 8, 2)
	writePtr(snap.Objects[1].Contents, 16, 1)

	_, err := DecodeLabelMap(mem, layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusMalformed {
		t.Fatalf("err = %v, want malformed", err)
	}
}

func TestDecodeLabelMapStringMissing(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	snap.Objects = snap.Objects[:3] // drop string objects
	layout, _ := LayoutFromSnapshot(snap, 0x18)

	_, err := DecodeLabelMap(NewMemory(snap), layout, Options{}, 0x1000)
	if err == nil || statusOf(err) != StatusStringMissing {
		t.Fatalf("err = %v, want string missing", err)
	}
}

func TestDecodeLabelsForGoroutine(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	got := DecodeAll(snap, Options{GLabelsOffset: 0x18, HasGLabelsOffset: true})

	if got.Stats.GoroutinesDecoded != 1 {
		t.Fatalf("decoded goroutines = %d", got.Stats.GoroutinesDecoded)
	}
	if got.LabelsByGID[123]["bubble"] != "alpha" {
		t.Fatalf("labelsByGID = %#v", got.LabelsByGID)
	}
}

func TestDecodeLabelsNoOffsetUnsupported(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	got := DecodeAll(snap, Options{})
	if got.Stats.GoroutinesUnsupported != 1 {
		t.Fatalf("unsupported = %d", got.Stats.GoroutinesUnsupported)
	}
	if got.Goroutines[0].Status != StatusUnsupportedRuntime {
		t.Fatalf("status = %s", got.Goroutines[0].Status)
	}
}

func TestDecodeLabelsNoLabels(t *testing.T) {
	snap := syntheticLabelSnapshot(0x18, []kv{{"bubble", "alpha"}})
	writePtr(snap.Objects[0].Contents, 0x18, 0)
	got := DecodeAll(snap, Options{GLabelsOffset: 0x18, HasGLabelsOffset: true})
	if got.Stats.GoroutinesNoLabels != 1 || got.Goroutines[0].Status != StatusNoLabels {
		t.Fatalf("result = %#v", got)
	}
}

func TestFindCandidateGLabelsOffsets(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}, {"job", "42"}})
	candidates := FindCandidateGLabelsOffsets(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if len(candidates) != 1 || candidates[0] != 0x20 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestFindCandidateGLabelsOffsetsAmbiguous(t *testing.T) {
	snap := syntheticLabelSnapshot(0x20, []kv{{"bubble", "alpha"}})
	writePtr(snap.Objects[0].Contents, 0x28, 0x1000)
	candidates := FindCandidateGLabelsOffsets(snap, NewMemory(snap), map[string]string{"bubble": "alpha"}, Options{})
	if len(candidates) != 2 || candidates[0] != 0x20 || candidates[1] != 0x28 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

type kv struct {
	k string
	v string
}

func syntheticLabelSnapshot(gLabelsOffset uint64, labels []kv) *heapsnapshot.HeapSnapshot {
	ptrSize := 8
	labelMap := make([]byte, 24)
	labelArray := make([]byte, len(labels)*32)
	writePtr(labelMap, 0, 0x2000)
	writePtr(labelMap, 8, uint64(len(labels)))
	writePtr(labelMap, 16, uint64(len(labels)))

	objects := []heapsnapshot.Object{
		{Addr: 0x5000, Contents: make([]byte, 0x80)},
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

func writeStringHeader(buf []byte, off int, addr uint64, s string) {
	writePtr(buf, off, addr)
	writePtr(buf, off+8, uint64(len(s)))
}

func writePtr(buf []byte, off int, value uint64) {
	binary.LittleEndian.PutUint64(buf[off:off+8], value)
}
