//go:build freebsd

package addrspace

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// probeStr is a string literal used to verify that ReadAtAddr can recover
// string bytes from the binary's read-only data section.
const probeStr = "addrspace-freebsd-probe-a3c92e1d"

func TestProcessReader_ReadsLiteralString(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(probeStr))))
	got, ok := r.ReadAtAddr(addr, uint64(len(probeStr)))
	if !ok {
		t.Fatalf("ReadAtAddr(0x%x, %d) failed; procfs may not be mounted and binary may be PIE", addr, len(probeStr))
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

	// Only the procfs path enforces the read-only filter. On the ELF fallback
	// path a heap address simply won't match any PT_LOAD Vaddr and will also
	// return false, so both paths must reject this read.
	payload := []byte("heap-backed-freebsd-test")
	s := string(payload)
	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(s))))

	if _, ok := r.ReadAtAddr(addr, uint64(len(s))); ok {
		t.Fatalf("ProcessReader must reject heap-backed (writable) address 0x%x", addr)
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

	suffix := probeStr[len(probeStr)-4:]
	addr := uint64(uintptr(unsafe.Pointer(unsafe.StringData(probeStr)))) + uint64(len(probeStr)-4)
	got, ok := r.ReadAtAddr(addr, uint64(len(suffix)))
	if !ok {
		t.Fatalf("ReadAtAddr partial suffix at 0x%x failed; procfs may not be mounted and binary may be PIE", addr)
	}
	if string(got) != suffix {
		t.Fatalf("got %q, want %q", string(got), suffix)
	}
}

func TestProcessReader_MappingsNonNilOnProcfsPath(t *testing.T) {
	r, err := OpenSelfProcessReader()
	if err != nil {
		t.Fatalf("OpenSelfProcessReader: %v", err)
	}
	defer r.Close()

	// If we are on the procfs path (mem != nil), Mappings must be non-empty.
	// On the ELF fallback path Mappings returns nil, which is acceptable.
	if r.mem == nil {
		t.Skip("not on procfs path; Mappings() nil is expected on ELF fallback")
	}
	if got := r.Mappings(); len(got) == 0 {
		t.Fatal("Mappings() returned empty slice on procfs path")
	}
}

func TestParseProcMap(t *testing.T) {
	in := strings.NewReader("" +
		"0x400000 0x41d000 5 0 0xfffff80012abc000 r-x 1 0 0x1000 COW NC vnode /usr/local/bin/foo NCH -1\n" +
		"0x41d000 0x41e000 1 1 0xfffff80012abc000 rw- 1 0 0x1000 COW NC vnode /usr/local/bin/foo NCH -1\n" +
		"0x41e000 0x41f000 1 1 0xfffff8001abcd000 rw- 1 1 0x0000 COW NNC anon - NCH -1\n" +
		"0xbfbdf000 0xbfc00000 33 33 0xc27a1580 --- 1 0 0x0000 COW NC anon - NCH -1\n" +
		"garbage line\n" +
		"0x500000 0x501000 1 0 0xfffff80099999999 r-- 1 0 0x1000 COW NC vnode /usr/share/lib/libc.so NCH -1\n" +
		"\n")
	got, err := parseFreeBSDProcMap(in)
	if err != nil {
		t.Fatalf("parseFreeBSDProcMap: %v", err)
	}
	want := []Mapping{
		{Start: 0x400000, End: 0x41d000, Read: true, Write: false, Exec: true, Path: "/usr/local/bin/foo"},
		{Start: 0x41d000, End: 0x41e000, Read: true, Write: true, Exec: false, Path: "/usr/local/bin/foo"},
		{Start: 0x41e000, End: 0x41f000, Read: true, Write: true, Exec: false, Path: ""},
		{Start: 0xbfbdf000, End: 0xbfc00000, Read: false, Write: false, Exec: false, Path: ""},
		{Start: 0x500000, End: 0x501000, Read: true, Write: false, Exec: false, Path: "/usr/share/lib/libc.so"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFreeBSDProcMap mismatch.\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseProcMap_EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		count int
	}{
		{
			name:  "too few fields",
			input: "0x400000 0x401000 1 0 0xdeadbeef\n",
			count: 0,
		},
		{
			name:  "bad hex start",
			input: "0xzzzz 0x401000 1 0 0xdeadbeef r-x 1 0 0x0 COW NC anon - NCH -1\n",
			count: 0,
		},
		{
			name:  "bad hex end",
			input: "0x400000 0xzzzz 1 0 0xdeadbeef r-x 1 0 0x0 COW NC anon - NCH -1\n",
			count: 0,
		},
		{
			name:  "end <= start",
			input: "0x401000 0x400000 1 0 0xdeadbeef r-x 1 0 0x0 COW NC anon - NCH -1\n",
			count: 0,
		},
		{
			name:  "end == start",
			input: "0x400000 0x400000 0 0 0xdeadbeef r-x 1 0 0x0 COW NC anon - NCH -1\n",
			count: 0,
		},
		{
			name:  "perms too short",
			input: "0x400000 0x401000 1 0 0xdeadbeef r 1 0 0x0 COW NC anon - NCH -1\n",
			count: 0,
		},
		{
			name:  "empty line",
			input: "\n",
			count: 0,
		},
		{
			name:  "valid minimal (6 fields)",
			input: "0x400000 0x401000 1 0 0xdeadbeef r-x\n",
			count: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFreeBSDProcMap(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("parseFreeBSDProcMap: %v", err)
			}
			if len(got) != tc.count {
				t.Fatalf("len = %d, want %d (got %+v)", len(got), tc.count, got)
			}
		})
	}
}

func TestMappingEligibleForStringBody(t *testing.T) {
	cases := []struct {
		name string
		m    Mapping
		want bool
	}{
		{"r-- (rodata)", Mapping{Read: true}, true},
		{"r-x (text)", Mapping{Read: true, Exec: true}, true},
		{"rw- (heap/stack)", Mapping{Read: true, Write: true}, false},
		{"rwx", Mapping{Read: true, Write: true, Exec: true}, false},
		{"--- (no perms)", Mapping{}, false},
		{"--x (exec only)", Mapping{Exec: true}, false},
	}
	for _, tc := range cases {
		if got := mappingEligibleForStringBody(tc.m); got != tc.want {
			t.Errorf("%s: mappingEligibleForStringBody=%v, want %v", tc.name, got, tc.want)
		}
	}
}
