package runtimelayout

import "testing"

// TestLocalInputHasVerifiedLayout is the toolchain tripwire: the pre-flight
// in memusage.Computer.Compute rejects requests with unsupported_runtime
// when Lookup(LocalInput()) misses, so a miss for the toolchain running the
// tests means /debug/memusage is broken on it. This fails on a Go upgrade
// that outruns the verified layout table.
func TestLocalInputHasVerifiedLayout(t *testing.T) {
	input := LocalInput()
	if _, ok := Lookup(input); !ok {
		t.Fatalf("no verified runtime.g.labels layout for the test toolchain (go=%s arch=%s ptrSize=%d bigEndian=%t); "+
			"run `go run ./cmd/labeloffsetprobe` and add the printed TableEntry to internal/runtimelayout/table.go",
			input.GoVersion, input.GOARCH, input.PtrSize, input.BigEndian)
	}
}
