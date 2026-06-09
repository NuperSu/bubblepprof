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

// probeMutable lives in the writable .data section. Its on-disk bytes are
// init-time state, not runtime state, so serving them would silently return
// stale data — the reader must reject writable-section addresses entirely,
// matching the Linux read-only-mapping and ELF non-writable-segment policy.
var probeMutable = [32]byte{'a', 'd', 'd', 'r', 's', 'p', 'a', 'c', 'e', '-', 'm', 'u', 't', 'a', 'b', 'l', 'e'}

func TestProcessReader_RejectsWritableSectionAddress(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	probeMutable[17] = 'X' // ensure runtime bytes differ from on-disk bytes
	addr := uint64(uintptr(unsafe.Pointer(&probeMutable[0])))
	if got, ok := r.ReadAtAddr(addr, uint64(len(probeMutable))); ok {
		t.Fatalf("ProcessReader must reject writable-section address 0x%x, served %q", addr, got)
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

func TestProcessReader_SlideIsPlausible(t *testing.T) {
	// The ASLR slide (GetModuleHandle - preferredImageBase) must be within the
	// 64-bit Windows user address space (~128 TB). Go 1.24+ enables High Entropy
	// ASLR so the slide routinely exceeds ±4 GB on 64-bit targets; ±128 TB
	// covers the full user-space range.
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	const maxSlide = int64(128) << 40 // 128 TB
	if r.slide < -maxSlide || r.slide > maxSlide {
		t.Fatalf("slide = %d, expected within ±128 TB", r.slide)
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
