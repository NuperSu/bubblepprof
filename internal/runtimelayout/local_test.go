package runtimelayout

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestLocalInput(t *testing.T) {
	input := LocalInput()
	if input.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", input.GoVersion, runtime.Version())
	}
	if input.GOARCH != runtime.GOARCH {
		t.Fatalf("GOARCH = %q, want %q", input.GOARCH, runtime.GOARCH)
	}
	if want := int(unsafe.Sizeof(uintptr(0))); input.PtrSize != want {
		t.Fatalf("PtrSize = %d, want %d", input.PtrSize, want)
	}
	// All currently supported test platforms are little-endian; more
	// importantly, the value must agree with what the heap dump writer
	// reports for this process, which Lookup uses as a key.
	var probe = [2]byte{1, 0}
	bigEndian := *(*uint16)(unsafe.Pointer(&probe[0])) != 1
	if input.BigEndian != bigEndian {
		t.Fatalf("BigEndian = %t, want %t", input.BigEndian, bigEndian)
	}
}
