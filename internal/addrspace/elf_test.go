package addrspace

import (
	"debug/elf"
	"math"
	"os"
	"testing"
)

func TestOpenELFReader_Missing(t *testing.T) {
	if _, err := OpenELFReader("/definitely/does/not/exist"); err == nil {
		t.Fatal("expected error opening missing file")
	}
}

func TestELFReader_Name_NonNil(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	r, err := OpenELFReader(exe)
	if err != nil {
		t.Skipf("OpenELFReader failed: %v", err)
	}
	defer r.Close()
	if got := r.Name(); got == "" || got == "elf" {
		t.Fatalf("Name() = %q, want 'elf:<path>'", got)
	}
}

func TestOpenELFReader_SelfExe(t *testing.T) {
	// The Go test binary is itself an ELF executable on Linux. Skip on
	// platforms where the test binary may not be ELF.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	r, err := OpenELFReader(exe)
	if err != nil {
		t.Skipf("OpenELFReader(test exe) failed (non-ELF host?): %v", err)
	}
	defer r.Close()

	segments := r.Segments()
	if len(segments) == 0 {
		t.Fatal("expected at least one PT_LOAD segment in test binary")
	}
	// Writable segments hold init-time bytes on disk, not runtime state;
	// indexing them could silently serve stale string bodies.
	for _, s := range segments {
		if s.Flags&elf.PF_W != 0 {
			t.Fatalf("writable PT_LOAD segment indexed: %+v", s)
		}
	}
	if _, ok := r.ReadAtAddr(0, 8); ok {
		t.Fatal("read at addr=0 must fail")
	}
	if got, ok := r.ReadAtAddr(0xdead, 0); !ok || len(got) != 0 {
		t.Fatalf("zero-size read = %v ok=%t", got, ok)
	}

	// Read first 8 bytes of the first readable file-backed segment by
	// its virtual address: this should succeed because the file is
	// loaded with the segment's Vaddr as its base in the ELF file
	// itself (the on-disk binary lacks a runtime load bias).
	s := segments[0]
	got, ok := r.ReadAtAddr(s.Vaddr, 8)
	if !ok {
		t.Skipf("ReadAtAddr at segment Vaddr 0x%x failed (PIE?); test binary may be relocated", s.Vaddr)
	}
	if len(got) != 8 {
		t.Fatalf("read length = %d, want 8", len(got))
	}
}

// TestELFReader_ReadAtAddr_FileOffOverflow exercises the fileOff > math.MaxInt64
// guard by injecting a segment with Off == math.MaxInt64 so addr > Vaddr makes
// fileOff overflow an int64.
func TestELFReader_ReadAtAddr_FileOffOverflow(t *testing.T) {
	f, err := os.CreateTemp("", "elf-off-overflow-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	r := &ELFReader{
		file: f,
		path: f.Name(),
		segments: []ELFSegment{
			{
				Vaddr:  100,
				Filesz: 1000,
				Memsz:  1000,
				Off:    math.MaxInt64, // Off + (101-100) = MaxInt64+1 > MaxInt64
			},
		},
	}
	if _, ok := r.ReadAtAddr(101, 8); ok {
		t.Fatal("fileOff overflow must return false")
	}
}

// TestELFReader_ReadAtAddr_SegmentEndOverflow exercises the AddUint64 overflow
// guard on Vaddr+Filesz by injecting a segment with Filesz == MaxUint64.
func TestELFReader_ReadAtAddr_SegmentEndOverflow(t *testing.T) {
	f, err := os.CreateTemp("", "elf-segend-overflow-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	r := &ELFReader{
		file: f,
		path: f.Name(),
		segments: []ELFSegment{
			{
				Vaddr:  0x1000,
				Filesz: math.MaxUint64, // 0x1000 + MaxUint64 overflows → segment skipped
				Memsz:  math.MaxUint64,
				Off:    0,
			},
		},
	}
	// The segment overflows so it is skipped; the addr is not matched.
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("segment end overflow must cause segment to be skipped")
	}
}

func TestELFReader_ReadAtAddr_NilSafe(t *testing.T) {
	var r *ELFReader
	if _, ok := r.ReadAtAddr(0x1000, 4); ok {
		t.Fatal("nil receiver must fail")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if r.Name() != "elf" {
		t.Fatalf("nil Name = %q", r.Name())
	}
	if r.Segments() != nil {
		t.Fatal("nil Segments must be nil")
	}
}
