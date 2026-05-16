package runtimelayout

import "testing"

// TestVerifiedTableShape locks the verified table to known entries so an
// accidental layout change (e.g. wrong offset, wrong source tag) shows up
// as a test failure here instead of as silently-wrong production output.
func TestVerifiedTableShape(t *testing.T) {
	if len(verifiedTable) != 1 {
		t.Fatalf("verifiedTable size = %d, want 1; add a regression test if expanding", len(verifiedTable))
	}
	e := verifiedTable[0]
	if e.VersionPrefix != "go1.26." {
		t.Fatalf("VersionPrefix = %q, want %q", e.VersionPrefix, "go1.26.")
	}
	if e.GOARCH != "amd64" {
		t.Fatalf("GOARCH = %q", e.GOARCH)
	}
	if e.PtrSize != 8 {
		t.Fatalf("PtrSize = %d", e.PtrSize)
	}
	if e.BigEndian {
		t.Fatal("BigEndian = true, want false")
	}
	if e.Layout.Source != SourceTable {
		t.Fatalf("Layout.Source = %q", e.Layout.Source)
	}
	if e.Layout.GLabelsOffset != 0x160 {
		t.Fatalf("Layout.GLabelsOffset = %#x", e.Layout.GLabelsOffset)
	}
	if e.Layout.LabelSize != 32 {
		t.Fatalf("Layout.LabelSize = %d", e.Layout.LabelSize)
	}
}
