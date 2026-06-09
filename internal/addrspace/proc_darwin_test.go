//go:build darwin

package addrspace

import (
	"testing"
	"unsafe"
)

// probeStr is a string literal used to verify that ReadAtAddr can
// recover string bytes from the binary's read-only data segment.
// The value is intentionally unique so it is unlikely to appear in
// unrelated rodata.
const probeStr = "addrspace-darwin-probe-c7f21a9e"

func TestProcessReader_ReadsLiteralString(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(probeStr))))
	got, ok := r.ReadAtAddr(addr, uint64(len(probeStr)))
	if !ok {
		t.Fatalf("ReadAtAddr(0x%x, %d) failed; slide computation or segment lookup may be wrong", addr, len(probeStr))
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
	payload := []byte("heap-backed-darwin-test")
	s := string(payload)
	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(s))))

	if _, ok := r.ReadAtAddr(addr, uint64(len(s))); ok {
		t.Fatalf("ProcessReader must reject heap-backed address 0x%x", addr)
	}
}

// probeMutable lives in the writable __DATA segment. Its on-disk bytes are
// init-time state, not runtime state, so serving them would silently return
// stale data — the reader must reject writable-segment addresses entirely,
// matching the Linux read-only-mapping and ELF non-writable-segment policy.
// (The equivalent omission on Windows was caught by mutation testing under
// wine: with the write filter removed, ReadAtAddr served the on-disk init
// bytes of a global that had been modified at runtime.)
var probeMutable = [32]byte{'a', 'd', 'd', 'r', 's', 'p', 'a', 'c', 'e', '-', 'm', 'u', 't', 'a', 'b', 'l', 'e'}

func TestProcessReader_RejectsWritableSegmentAddress(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	probeMutable[17] = 'X' // ensure runtime bytes differ from on-disk bytes
	addr := uint64(uintptr(unsafe.Pointer(&probeMutable[0])))
	if got, ok := r.ReadAtAddr(addr, uint64(len(probeMutable))); ok {
		t.Fatalf("ProcessReader must reject writable-segment address 0x%x, served %q", addr, got)
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

func TestProcessReader_ReadsPartialSegment(t *testing.T) {
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

func TestComputeMachOSlide_IsNonNegative(t *testing.T) {
	// On macOS with ASLR, the slide is always >= 0 (runtime address >=
	// on-disk vmaddr). A negative slide would indicate a bug in the
	// pclntab lookup or an unusual binary layout.
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if r.slide < 0 {
		t.Fatalf("slide = %d, expected >= 0", r.slide)
	}
}

func TestProcessReader_MappingsNilOnDarwin(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if got := r.Mappings(); got != nil {
		t.Fatalf("Mappings() = %v, want nil on Darwin", got)
	}
}

func TestProcessReader_SegmentsAreReadOnly(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()
	if len(r.segments) == 0 {
		t.Fatal("no read-only segments found; binary may lack rodata")
	}
	// Sanity: all indexed segments should have non-zero filesz.
	for i, s := range r.segments {
		if s.filesz == 0 {
			t.Errorf("segment[%d] has filesz=0", i)
		}
	}
}

func TestOpenSelfProcessReader_SlideProbeRoundTrip(t *testing.T) {
	// Integration check: the slide is correct when we can independently
	// read multiple known rodata strings. A wrong slide is unlikely to
	// satisfy more than one probe, so a consistent multi-string round-trip
	// verifies more than the single-string read in TestProcessReader_ReadsLiteralString.
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	const secondProbe = "addrspace-darwin-second-probe-9b3f4d21"
	for _, s := range []string{probeStr, secondProbe} {
		addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(s))))
		got, ok := r.ReadAtAddr(addr, uint64(len(s)))
		if !ok {
			t.Fatalf("ReadAtAddr(%q) at 0x%x failed; slide may be wrong", s, addr)
		}
		if string(got) != s {
			t.Fatalf("ReadAtAddr(%q) returned %q", s, string(got))
		}
	}
}
