package snapshotparse

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshot"
)

func TestParseSnapshotFromBundle(t *testing.T) {
	heapDump := buildHeapDump(t)

	var tarBuf bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tarBuf, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heapDump),
		HeapDumpSize:     int64(len(heapDump)),
		GoroutineProfile: []byte("fake profile"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  4242,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
			GCBeforeHeapDump:     true,
		},
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	res, err := ParseSnapshot(&tarBuf, heapdump.Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("format = %q", res.Metadata.Format)
	}
	if res.HeapDumpSize != int64(len(heapDump)) {
		t.Fatalf("heap size = %d, want %d", res.HeapDumpSize, len(heapDump))
	}
	if res.Snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if res.Snapshot.Stats.ObjectCount == 0 {
		t.Fatalf("expected object, got stats %+v", res.Snapshot.Stats)
	}
}

func TestParseSnapshotMissingHeapDump(t *testing.T) {
	heapDump := buildHeapDump(t)
	_ = heapDump // unused on purpose
	var tarBuf bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tarBuf, snapshot.BundleSource{
		HeapDump:         strings.NewReader(""),
		HeapDumpSize:     0,
		GoroutineProfile: []byte("fake"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  4242,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	if _, err := ParseSnapshot(&tarBuf, heapdump.Options{}); err == nil {
		t.Fatal("expected error for empty heap dump")
	}
}

func TestParseSnapshotUnsupportedFormat(t *testing.T) {
	heapDump := buildHeapDump(t)
	var tarBuf bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tarBuf, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heapDump),
		HeapDumpSize:     int64(len(heapDump)),
		GoroutineProfile: []byte("fake"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               "some-old-format",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	if _, err := ParseSnapshot(&tarBuf, heapdump.Options{}); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

// buildHeapDump builds a minimal synthetic heap dump byte stream.
func buildHeapDump(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("go1.7 heap dump\n")

	// params record: tag, bool, uvarint x 4, string, string, uvarint
	writeUvarint(&buf, 6) // tagParams
	writeUvarint(&buf, 0) // bigEndian=false
	writeUvarint(&buf, 8) // ptr size
	writeUvarint(&buf, 0) // heap start
	writeUvarint(&buf, 0) // heap end
	writeString(&buf, "amd64")
	writeString(&buf, "go-test")
	writeUvarint(&buf, 1) // num cpu

	// One object with one zero pointer.
	contents := make([]byte, 8)
	binary.LittleEndian.PutUint64(contents, 0x1234)
	writeUvarint(&buf, 1) // tagObject
	writeUvarint(&buf, 0x100)
	writeUvarint(&buf, uint64(len(contents)))
	buf.Write(contents)
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(&buf, 0)
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindEol))

	writeUvarint(&buf, 0) // tagEOF
	return buf.Bytes()
}

func writeUvarint(buf *bytes.Buffer, x uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], x)
	buf.Write(tmp[:n])
}

func writeString(buf *bytes.Buffer, s string) {
	writeUvarint(buf, uint64(len(s)))
	buf.WriteString(s)
}
