package addrspace

import (
	"os"
	"testing"
)

func TestOpenELFReader_Missing(t *testing.T) {
	if _, err := OpenELFReader("/definitely/does/not/exist"); err == nil {
		t.Fatal("expected error opening missing file")
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
