package runtimelayout

import (
	"encoding/binary"
	"testing"
)

func TestManualPtrSize8LittleEndian(t *testing.T) {
	layout, err := Manual(LookupInput{
		GoVersion: "go1.99-dev",
		GOARCH:    "amd64",
		PtrSize:   8,
		BigEndian: false,
	}, 0x160)
	if err != nil {
		t.Fatalf("Manual: %v", err)
	}
	if layout.Source != SourceManual {
		t.Fatalf("Source = %q, want %q", layout.Source, SourceManual)
	}
	if layout.GLabelsOffset != 0x160 {
		t.Fatalf("GLabelsOffset = %#x", layout.GLabelsOffset)
	}
	if layout.PtrSize != 8 {
		t.Fatalf("PtrSize = %d", layout.PtrSize)
	}
	if layout.GoVersion != "go1.99-dev" {
		t.Fatalf("GoVersion = %q", layout.GoVersion)
	}
	if layout.SliceLenOffset != 8 || layout.SliceCapOffset != 16 {
		t.Fatalf("slice offsets = %d/%d", layout.SliceLenOffset, layout.SliceCapOffset)
	}
	if layout.StringLenOffset != 8 {
		t.Fatalf("string len offset = %d", layout.StringLenOffset)
	}
	if layout.LabelValueOffset != 16 || layout.LabelSize != 32 {
		t.Fatalf("label value offset/size = %d/%d", layout.LabelValueOffset, layout.LabelSize)
	}
	if layout.ByteOrder() != binary.LittleEndian {
		t.Fatal("ByteOrder mismatch")
	}
}

func TestManualRejectsUnsupportedPtrSize(t *testing.T) {
	for _, ps := range []int{0, 4, 16} {
		if _, err := Manual(LookupInput{PtrSize: ps}, 0x10); err == nil {
			t.Errorf("Manual ptrSize %d: expected error", ps)
		}
	}
}

func TestManualRejectsBigEndian(t *testing.T) {
	_, err := Manual(LookupInput{PtrSize: 8, BigEndian: true}, 0x10)
	if err == nil {
		t.Fatal("Manual big-endian: expected error")
	}
}

func TestByteOrderBigEndian(t *testing.T) {
	if (Layout{BigEndian: true}).ByteOrder() != binary.BigEndian {
		t.Fatal("expected big-endian byte order")
	}
}
