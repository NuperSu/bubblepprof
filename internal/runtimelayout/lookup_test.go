package runtimelayout

import (
	"strings"
	"testing"
)

func TestLookupVerifiedGo126AMD64(t *testing.T) {
	cases := []string{
		"go1.26.0",
		"go1.26.3",
		"go1.26.3-X:nodwarf5",
		"go1.26.999",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{
				GoVersion: v,
				GOARCH:    "amd64",
				PtrSize:   8,
				BigEndian: false,
			})
			if !ok {
				t.Fatalf("Lookup(%q) returned false", v)
			}
			if layout.Source != SourceTable {
				t.Fatalf("Source = %q, want %q", layout.Source, SourceTable)
			}
			if layout.GLabelsOffset != 0x160 {
				t.Fatalf("GLabelsOffset = %#x, want 0x160", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q (Lookup must echo input)", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupMisses(t *testing.T) {
	cases := []LookupInput{
		{GoVersion: "go1.27.0", GOARCH: "amd64", PtrSize: 8},
		{GoVersion: "go1.26.3", GOARCH: "amd64", PtrSize: 4},
		{GoVersion: "go1.26.3", GOARCH: "amd64", PtrSize: 8, BigEndian: true},
		{GoVersion: "", GOARCH: "amd64", PtrSize: 8},
		{GoVersion: "go1.23.0", GOARCH: "amd64", PtrSize: 8},
	}
	for _, c := range cases {
		if _, ok := Lookup(c); ok {
			t.Fatalf("Lookup(%+v) returned true, want false", c)
		}
	}
}

func TestLookupVerifiedGo124AMD64(t *testing.T) {
	cases := []string{
		"go1.24.0",
		"go1.24.3",
		"go1.24.999",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{
				GoVersion: v,
				GOARCH:    "amd64",
				PtrSize:   8,
				BigEndian: false,
			})
			if !ok {
				t.Fatalf("Lookup(%q) returned false", v)
			}
			if layout.GLabelsOffset != 0x160 {
				t.Fatalf("GLabelsOffset = %#x, want 0x160", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupVerifiedGo125AMD64(t *testing.T) {
	cases := []string{
		"go1.25.0",
		"go1.25.3",
		"go1.25.999",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{
				GoVersion: v,
				GOARCH:    "amd64",
				PtrSize:   8,
				BigEndian: false,
			})
			if !ok {
				t.Fatalf("Lookup(%q) returned false", v)
			}
			if layout.GLabelsOffset != 0x158 {
				t.Fatalf("GLabelsOffset = %#x, want 0x158", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupVerifiedGo126ARM64(t *testing.T) {
	cases := []string{"go1.26.0", "go1.26.3", "go1.26.999"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{GoVersion: v, GOARCH: "arm64", PtrSize: 8})
			if !ok {
				t.Fatalf("Lookup(%q arm64) returned false", v)
			}
			if layout.GLabelsOffset != 0x160 {
				t.Fatalf("GLabelsOffset = %#x, want 0x160", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupVerifiedGo125ARM64(t *testing.T) {
	cases := []string{"go1.25.0", "go1.25.3", "go1.25.999"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{GoVersion: v, GOARCH: "arm64", PtrSize: 8})
			if !ok {
				t.Fatalf("Lookup(%q arm64) returned false", v)
			}
			if layout.GLabelsOffset != 0x158 {
				t.Fatalf("GLabelsOffset = %#x, want 0x158", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupVerifiedGo124ARM64(t *testing.T) {
	cases := []string{"go1.24.0", "go1.24.3", "go1.24.999"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			layout, ok := Lookup(LookupInput{GoVersion: v, GOARCH: "arm64", PtrSize: 8})
			if !ok {
				t.Fatalf("Lookup(%q arm64) returned false", v)
			}
			if layout.GLabelsOffset != 0x160 {
				t.Fatalf("GLabelsOffset = %#x, want 0x160", layout.GLabelsOffset)
			}
			if layout.GoVersion != v {
				t.Fatalf("GoVersion = %q, want %q", layout.GoVersion, v)
			}
		})
	}
}

func TestLookupVerifiedLayoutValues(t *testing.T) {
	layout, ok := Lookup(LookupInput{
		GoVersion: "go1.26.3",
		GOARCH:    "amd64",
		PtrSize:   8,
		BigEndian: false,
	})
	if !ok {
		t.Fatal("Lookup failed for verified target")
	}
	if layout.PtrSize != 8 {
		t.Fatalf("PtrSize = %d, want 8", layout.PtrSize)
	}
	if layout.BigEndian {
		t.Fatal("BigEndian = true, want false")
	}
	if layout.GLabelsOffset != 0x160 {
		t.Fatalf("GLabelsOffset = %#x, want 0x160", layout.GLabelsOffset)
	}
	if layout.SliceDataOffset != 0 || layout.SliceLenOffset != 8 || layout.SliceCapOffset != 16 {
		t.Fatalf("slice offsets = %d/%d/%d, want 0/8/16",
			layout.SliceDataOffset, layout.SliceLenOffset, layout.SliceCapOffset)
	}
	if layout.StringDataOffset != 0 || layout.StringLenOffset != 8 {
		t.Fatalf("string offsets = %d/%d, want 0/8",
			layout.StringDataOffset, layout.StringLenOffset)
	}
	if layout.LabelKeyOffset != 0 || layout.LabelValueOffset != 16 {
		t.Fatalf("label key/value offsets = %d/%d, want 0/16",
			layout.LabelKeyOffset, layout.LabelValueOffset)
	}
	if layout.LabelSize != 32 {
		t.Fatalf("LabelSize = %d, want 32", layout.LabelSize)
	}
}

func TestUnsupportedMessageMentionsInputs(t *testing.T) {
	msg := UnsupportedMessage(LookupInput{
		GoVersion: "go1.27.0",
		GOARCH:    "ppc64le",
		PtrSize:   8,
		BigEndian: true,
	})
	for _, want := range []string{"go1.27.0", "ppc64le", "ptr size 8", "big endian"} {
		if !strings.Contains(msg, want) {
			t.Errorf("UnsupportedMessage missing %q: %s", want, msg)
		}
	}
}

func TestHasVersionPrefix(t *testing.T) {
	cases := []struct {
		version string
		prefix  string
		want    bool
	}{
		{"go1.26.3", "go1.26.", true},
		{"go1.26.3-X:nodwarf5", "go1.26.", true},
		{"go1.26.", "go1.26.", true},
		{"go1.25.0", "go1.26.", false},
		{"go", "go1.26.", false},
		{"", "go1.26.", false},
	}
	for _, c := range cases {
		if got := hasVersionPrefix(c.version, c.prefix); got != c.want {
			t.Errorf("hasVersionPrefix(%q, %q) = %t, want %t", c.version, c.prefix, got, c.want)
		}
	}
}
