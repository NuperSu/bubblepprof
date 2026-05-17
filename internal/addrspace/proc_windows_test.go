//go:build windows

package addrspace

import (
	"testing"
	"unsafe"
)

// probeStr is a string literal used to verify that ReadAtAddr can
// recover string bytes from the binary's read-only data section.
// The value is intentionally unique so it is unlikely to appear in
// unrelated rodata.
const probeStr = "addrspace-windows-probe-d8e31b7f"

func TestProcessReader_ReadsLiteralString(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(probeStr))))
	got, ok := r.ReadAtAddr(addr, uint64(len(probeStr)))
	if !ok {
		t.Fatalf("ReadAtAddr(0x%x, %d) failed; slide computation or section lookup may be wrong", addr, len(probeStr))
	}
	if string(got) != probeStr {
		t.Fatalf("ReadAtAddr returned %q, want %q", string(got), probeStr)
	}
}

func TestProcessReader_RejectsNullAddr(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	if _, ok := r.ReadAtAddr(0, 8); ok {
		t.Fatal("read at addr=0 must fail")
	}
}

func TestProcessReader_ZeroSizeAlwaysOK(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	got, ok := r.ReadAtAddr(0xdead, 0)
	if !ok || len(got) != 0 {
		t.Fatalf("zero-size read = %v ok=%t, want ok=true empty slice", got, ok)
	}
}

func TestProcessReader_RejectsHeapBackedString(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	// Build a string whose data pointer is heap-backed (not rodata).
	payload := []byte("heap-backed-windows-test")
	s := string(payload)
	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(s))))

	if _, ok := r.ReadAtAddr(addr, uint64(len(s))); ok {
		t.Fatalf("ProcessReader must reject heap-backed address 0x%x", addr)
	}
}

func TestProcessReader_RejectsAddressOverflow(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	const maxUint64 = ^uint64(0)
	if _, ok := r.ReadAtAddr(maxUint64, 8); ok {
		t.Fatal("overflow read must return false")
	}
}

func TestProcessReader_CloseIsIdempotent(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("read after Close must fail")
	}
}

func TestProcessReader_Name(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if got := r.Name(); got != "process" {
		t.Fatalf("Name() = %q, want %q", got, "process")
	}
}

func TestProcessReader_NilReceiver(t *testing.T) {
	var r *ProcessReader
	if _, ok := r.ReadAtAddr(0x1000, 8); ok {
		t.Fatal("nil receiver ReadAtAddr must return false")
	}
	if got := r.Name(); got != "process" {
		t.Fatalf("nil Name() = %q", got)
	}
	if got := r.Mappings(); got != nil {
		t.Fatalf("nil Mappings() = %v", got)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}
}

func TestProcessReader_ReadsPartialSection(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	// Read a suffix of the probe string to verify offset arithmetic.
	suffix := probeStr[len(probeStr)-4:]
	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(probeStr)))) + uint64(len(probeStr)-4)
	got, ok := r.ReadAtAddr(addr, uint64(len(suffix)))
	if !ok {
		t.Fatalf("ReadAtAddr partial suffix at 0x%x failed", addr)
	}
	if string(got) != suffix {
		t.Fatalf("got %q, want %q", string(got), suffix)
	}
}

func TestComputePESlide_IsNonNegative(t *testing.T) {
	// On Windows with ASLR, the slide is always >= 0 (runtime address >=
	// on-disk virtual address). A negative slide would indicate a bug in
	// the pclntab lookup or an unusual binary layout.
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if r.slide < 0 {
		t.Fatalf("slide = %d, expected >= 0", r.slide)
	}
}

func TestProcessReader_MappingsNilOnWindows(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if got := r.Mappings(); got != nil {
		t.Fatalf("Mappings() = %v, want nil on Windows", got)
	}
}

func TestProcessReader_SectionsAreReadOnly(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if len(r.sections) == 0 {
		t.Fatal("no read-only sections found; binary may lack rodata")
	}
	for i, s := range r.sections {
		if s.size == 0 {
			t.Errorf("section[%d] has size=0", i)
		}
	}
}
