package capture

import (
	"os"
	"testing"
)

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
