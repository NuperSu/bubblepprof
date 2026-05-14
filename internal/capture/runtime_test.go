package capture

import (
	"bubblepprof/internal/bubblelabels"
	"bytes"
	"os"
	"testing"
)

// RuntimeHeapDumpWriter writes a binary heap dump produced by
// runtime/debug.WriteHeapDump into a file descriptor. Exercise the real
// implementation end-to-end in non-short mode.
func TestRuntimeHeapDumpWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real heap dump in short mode")
	}
	f, err := os.CreateTemp(t.TempDir(), "heap-*.dump")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if err := (RuntimeHeapDumpWriter{}).WriteHeapDump(f.Fd()); err != nil {
		t.Fatalf("WriteHeapDump: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("heap dump file is empty")
	}
}

// Confirm RegistryLabelManifestProvider with a nil Registry returns nil
// bytes without error.
func TestRegistryLabelManifestProviderNilRegistry(t *testing.T) {
	p := RegistryLabelManifestProvider{Registry: nil}
	b, err := p.LabelManifest()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if b != nil {
		t.Fatalf("expected nil bytes, got %d", len(b))
	}
}

// Registry has a pushed entry that was then popped before Snapshot: the
// manifest must be empty even though the registry stack map has the id.
func TestRegistryLabelManifestProviderPoppedStacks(t *testing.T) {
	r := bubblelabels.NewRegistry()
	pop := r.Push(7, map[string]string{"bubble": "alpha"})
	pop()
	p := RegistryLabelManifestProvider{Registry: r, Source: "x"}
	b, err := p.LabelManifest()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if b != nil {
		// Registry.Len() is still 1 because the map key exists with
		// empty stack; Snapshot must filter that out and produce nil.
		// Decode anyway to make sure it's a valid empty manifest.
		m, err := bubblelabels.DecodeManifest(b)
		if err != nil {
			t.Fatalf("DecodeManifest: %v", err)
		}
		if len(m.Goroutines) != 0 {
			t.Fatalf("expected empty goroutines, got %+v", m.Goroutines)
		}
	}
}

// RuntimeGoroutineProfileWriter must produce parseable pprof bytes.
func TestRuntimeGoroutineProfileBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := (RuntimeGoroutineProfileWriter{}).WriteGoroutineProfile(&buf); err != nil {
		t.Fatalf("WriteGoroutineProfile: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty profile bytes")
	}
	// pprof profiles start with a gzip header.
	if buf.Bytes()[0] != 0x1f || buf.Bytes()[1] != 0x8b {
		t.Fatalf("expected gzip-encoded pprof, got prefix %x", buf.Bytes()[:2])
	}
}
