package runtimelayout

import "testing"

// TestVerifiedTableShape locks the verified table to known entries so an
// accidental layout change (e.g. wrong offset, wrong source tag) shows up
// as a test failure here instead of as silently-wrong production output.
func TestVerifiedTableShape(t *testing.T) {
	type wantEntry struct {
		versionPrefix string
		goarch        string
		ptrSize       int
		gLabelsOffset uint64
	}
	want := []wantEntry{
		{"go1.26.", "amd64", 8, 0x160},
		{"go1.26.", "arm", 4, 0xd8},
		{"go1.25.", "amd64", 8, 0x158},
		{"go1.25.", "arm", 4, 0xd0},
		{"go1.24.", "amd64", 8, 0x160},
		{"go1.24.", "arm", 4, 0xd4},
	}
	if len(verifiedTable) != len(want) {
		t.Fatalf("verifiedTable size = %d, want %d; add a regression test if expanding", len(verifiedTable), len(want))
	}
	for i, w := range want {
		e := verifiedTable[i]
		if e.VersionPrefix != w.versionPrefix {
			t.Errorf("[%d] VersionPrefix = %q, want %q", i, e.VersionPrefix, w.versionPrefix)
		}
		if e.GOARCH != w.goarch {
			t.Errorf("[%d] GOARCH = %q, want %q", i, e.GOARCH, w.goarch)
		}
		if e.PtrSize != w.ptrSize {
			t.Errorf("[%d] PtrSize = %d, want %d", i, e.PtrSize, w.ptrSize)
		}
		if e.BigEndian {
			t.Errorf("[%d] BigEndian = true, want false", i)
		}
		if e.Layout.Source != SourceTable {
			t.Errorf("[%d] Layout.Source = %q", i, e.Layout.Source)
		}
		if e.Layout.GLabelsOffset != w.gLabelsOffset {
			t.Errorf("[%d] Layout.GLabelsOffset = %#x, want %#x", i, e.Layout.GLabelsOffset, w.gLabelsOffset)
		}
		wantLabelSize := uint64(w.ptrSize) * 4
		if e.Layout.LabelSize != wantLabelSize {
			t.Errorf("[%d] Layout.LabelSize = %d, want %d", i, e.Layout.LabelSize, wantLabelSize)
		}
	}
}
