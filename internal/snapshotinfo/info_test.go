package snapshotinfo

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
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

func TestPrintParseSummary(t *testing.T) {
	heap := buildMinimalHeapDump()
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  9999,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp snapshot: %v", err)
	}

	var out bytes.Buffer
	if err := PrintParse(&out, path); err != nil {
		t.Fatalf("print parse: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"snapshot format: bubblepprof-snapshot-v1",
		"heap dump header: go1.7 heap dump",
		"objects: 1",
		"goroutines: 1",
		"stack frames: 1",
		"data segments: 1",
		"bss segments: 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintGraphSummary(t *testing.T) {
	heap := buildMinimalHeapDump()
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  9999,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp snapshot: %v", err)
	}

	var out bytes.Buffer
	if err := PrintGraph(&out, path); err != nil {
		t.Fatalf("print graph: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"snapshot format: bubblepprof-snapshot-v1",
		"objects: 1",
		"goroutines: 1",
		"goroutine roots:",
		"global roots:",
		"unreachable objects:",
		"bubble attribution: not implemented in this phase",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"bogus"})
	if code != 2 {
		t.Fatalf("got exit %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "usage:") {
		t.Fatalf("expected usage message, got %q", errBuf.String())
	}
}

func buildMinimalHeapDump() []byte {
	var buf bytes.Buffer
	buf.WriteString("go1.7 heap dump\n")

	writeUvarint(&buf, 6) // tagParams
	writeUvarint(&buf, 0) // bigEndian=false
	writeUvarint(&buf, 8) // ptrSize
	writeUvarint(&buf, 0) // heap start
	writeUvarint(&buf, 0) // heap end
	writeString(&buf, "amd64")
	writeString(&buf, "go-test")
	writeUvarint(&buf, 1) // numcpu

	// One object containing one zero pointer.
	writeUvarint(&buf, 1) // tagObject
	writeUvarint(&buf, 0x100)
	writeBytes(&buf, make([]byte, 8))
	writeFields(&buf, []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}})

	// One goroutine + one stack frame.
	writeUvarint(&buf, 4) // tagGoroutine
	writeUvarint(&buf, 0xaa00)
	writeUvarint(&buf, 0xbb00)
	writeUvarint(&buf, 7) // goid
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0) // isSystem=false
	writeUvarint(&buf, 0) // isBackground=false
	writeUvarint(&buf, 0)
	writeString(&buf, "")
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)

	writeUvarint(&buf, 5) // tagStackFrame
	writeUvarint(&buf, 0xbb00)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeBytes(&buf, make([]byte, 8))
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeString(&buf, "main.fn")
	writeFields(&buf, nil)

	// One data segment.
	writeUvarint(&buf, 12) // tagData
	writeUvarint(&buf, 0xd0)
	writeBytes(&buf, make([]byte, 8))
	writeFields(&buf, nil)

	// One bss segment.
	writeUvarint(&buf, 13) // tagBSS
	writeUvarint(&buf, 0xe0)
	writeBytes(&buf, make([]byte, 8))
	writeFields(&buf, nil)

	writeUvarint(&buf, 0) // tagEOF
	return buf.Bytes()
}

func writeUvarint(buf *bytes.Buffer, x uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], x)
	buf.Write(tmp[:n])
}

func writeBytes(buf *bytes.Buffer, b []byte) {
	writeUvarint(buf, uint64(len(b)))
	buf.Write(b)
}

func writeString(buf *bytes.Buffer, s string) {
	writeBytes(buf, []byte(s))
}

func writeFields(buf *bytes.Buffer, fields []heapsnapshot.Field) {
	for _, f := range fields {
		writeUvarint(buf, uint64(f.Kind))
		writeUvarint(buf, f.Offset)
	}
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
}
