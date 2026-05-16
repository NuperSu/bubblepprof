package runtimelayout

import "testing"

// TestVerifiedTableShape locks the verified table to known entries so an
// accidental layout change (e.g. wrong offset, wrong source tag) shows up
// as a test failure here instead of as silently-wrong production output.
func TestVerifiedTableShape(t *testing.T) {
	type wantEntry struct {
		versionPrefix string
		gLabelsOffset uint64
	}
	want := []wantEntry{
		{"go1.26.", 0x160},
		{"go1.25.", 0x158},
		{"go1.24.", 0x160},
	}
	if len(verifiedTable) != len(want) {
		t.Fatalf("verifiedTable size = %d, want %d; add a regression test if expanding", len(verifiedTable), len(want))
	}
	for i, w := range want {
		e := verifiedTable[i]
		if e.VersionPrefix != w.versionPrefix {
			t.Errorf("[%d] VersionPrefix = %q, want %q", i, e.VersionPrefix, w.versionPrefix)
		}
		if e.GOARCH != "amd64" {
			t.Errorf("[%d] GOARCH = %q", i, e.GOARCH)
		}
		if e.PtrSize != 8 {
			t.Errorf("[%d] PtrSize = %d", i, e.PtrSize)
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
		if e.Layout.LabelSize != 32 {
			t.Errorf("[%d] Layout.LabelSize = %d", i, e.Layout.LabelSize)
		}
	}
}
