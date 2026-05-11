package tests

import (
	"bytes"
	"context"
	"os"
	"runtime/pprof"
	"testing"
)

// prepareGoroutine attaches a pprof label ("test" = t.Name()) to the current
// goroutine. This is the mechanism that would eventually allow grouping
// goroutines into pprof bubbles, though the analyzer does not read these
// labels yet.
func prepareGoroutine(t *testing.T) context.Context {
	t.Helper()

	ctx := pprof.WithLabels(t.Context(), pprof.Labels("test", t.Name()))
	pprof.SetGoroutineLabels(ctx)
	return ctx
}

// readHeapDumpFixture reads a file produced by runtime/debug.WriteHeapDump.
// This is NOT the same as a core dump; it is a Go-specific binary format
// documented at https://go.dev/src/runtime/heapdump.go. The tests currently
// only validate that the file exists, starts with the expected magic header,
// and contains an architecture marker. No parsing of the heap dump contents
// is performed — the actual analyzer (goheap) operates on core dumps via
// Delve, not on these heap dump files.
func readHeapDumpFixture(t *testing.T, envVar, fallbackPath string) []byte {
	t.Helper()

	path := fallbackPath
	if v := os.Getenv(envVar); v != "" {
		path = v
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heap dump %s: %v", path, err)
	}
	if len(b) < 256 {
		t.Fatalf("heap dump %s too small: %d bytes", path, len(b))
	}

	const heapDumpMagic = "go1.7 heap dump\n"
	if !bytes.HasPrefix(b, []byte(heapDumpMagic)) {
		t.Fatalf("heap dump %s missing magic %q", path, heapDumpMagic)
	}

	if !bytes.Contains(b, []byte("amd64")) {
		t.Fatalf("heap dump %s missing expected architecture marker", path)
	}

	return b
}
