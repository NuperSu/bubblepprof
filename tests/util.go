package tests

import (
	"bytes"
	"context"
	"os"
	"runtime/pprof"
	"testing"
)

func prepareGoroutine(t *testing.T) context.Context {
	t.Helper()

	ctx := pprof.WithLabels(t.Context(), pprof.Labels("test", t.Name()))
	pprof.SetGoroutineLabels(ctx)
	return ctx
}

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

	// runtime/debug.WriteHeapDump currently starts with this magic header.
	const heapDumpMagic = "go1.7 heap dump\n"
	if !bytes.HasPrefix(b, []byte(heapDumpMagic)) {
		t.Fatalf("heap dump %s missing magic %q", path, heapDumpMagic)
	}

	// Basic sanity check that target architecture metadata is present.
	if !bytes.Contains(b, []byte("amd64")) {
		t.Fatalf("heap dump %s missing expected architecture marker", path)
	}

	return b
}
