package runtimelayout

import "testing"

func TestLookupBestEffort_Found(t *testing.T) {
	// "go1.99.0" is not in the table, but amd64/8/little-endian is.
	input := LookupInput{
		GoVersion: "go1.99.0",
		GOARCH:    "amd64",
		PtrSize:   8,
		BigEndian: false,
	}
	layout, ok := LookupBestEffort(input)
	if !ok {
		t.Fatal("LookupBestEffort returned false for known arch/ptrSize")
	}
	if layout.GOARCH != "amd64" {
		t.Fatalf("GOARCH = %q, want amd64", layout.GOARCH)
	}
	if layout.GoVersion != "go1.99.0" {
		t.Fatalf("GoVersion = %q, want go1.99.0", layout.GoVersion)
	}
}

func TestLookupBestEffort_NotFound(t *testing.T) {
	// s390x is not in the table at all.
	input := LookupInput{
		GoVersion: "go1.99.0",
		GOARCH:    "s390x",
		PtrSize:   8,
		BigEndian: true,
	}
	_, ok := LookupBestEffort(input)
	if ok {
		t.Fatal("LookupBestEffort returned true for unknown arch")
	}
}

func TestLookupBestEffort_PtrSizeMismatch(t *testing.T) {
	// PtrSize=2 has no table entry on any platform.
	input := LookupInput{
		GoVersion: "go1.99.0",
		GOARCH:    "amd64",
		PtrSize:   2,
		BigEndian: false,
	}
	_, ok := LookupBestEffort(input)
	if ok {
		t.Fatal("LookupBestEffort returned true for PtrSize=2 (not in table)")
	}
}

func TestLookupBestEffort_BigEndianMismatch(t *testing.T) {
	// All table entries are little-endian; requesting BigEndian=true hits the BigEndian continue.
	input := LookupInput{
		GoVersion: "go1.99.0",
		GOARCH:    "amd64",
		PtrSize:   8,
		BigEndian: true, // no big-endian entries in table
	}
	_, ok := LookupBestEffort(input)
	if ok {
		t.Fatal("LookupBestEffort returned true for big-endian (not in table)")
	}
}

// TestVerifiedTableShape locks the verified table to known entries so an
// accidental layout change (e.g. wrong offset, wrong source tag) shows up
// as a test failure here instead of as silently-wrong production output.
func TestVerifiedTableShape(t *testing.T) {
	type wantEntry struct {
		versionPrefix string
		ptrSize       int
		gLabelsOffset uint64
	}
	want := []wantEntry{
		{"go1.26.", 8, 0x160},
		{"go1.26.", 4, 0xd8},
		{"go1.25.", 8, 0x158},
		{"go1.25.", 4, 0xd0},
		{"go1.24.", 8, 0x160},
		{"go1.24.", 4, 0xd4},
	}
	if len(verifiedTable) != len(want) {
		t.Fatalf("verifiedTable size = %d, want %d; add a regression test if expanding", len(verifiedTable), len(want))
	}
	for i, w := range want {
		e := verifiedTable[i]
		if e.VersionPrefix != w.versionPrefix {
			t.Errorf("[%d] VersionPrefix = %q, want %q", i, e.VersionPrefix, w.versionPrefix)
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
