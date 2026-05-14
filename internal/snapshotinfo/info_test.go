package snapshotinfo

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"bubblepprof/internal/snapshot"
)

func TestPrintUsesSnapshotSizes(t *testing.T) {
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, snapshot.BundleSource{
		HeapDump:         strings.NewReader("heap data"),
		HeapDumpSize:     int64(len("heap data")),
		GoroutineProfile: []byte("profile data"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  123,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
			GCBeforeHeapDump:     true,
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp snapshot: %v", err)
	}

	var out bytes.Buffer
	if err := Print(&out, path); err != nil {
		t.Fatalf("print info: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"format: bubblepprof-snapshot-v1",
		"heap.dump: present, 9 bytes",
		"goroutine.pprof: present, 12 bytes",
		"metadata.json: valid",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}
