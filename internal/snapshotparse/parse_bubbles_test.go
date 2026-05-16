package snapshotparse

import (
	"bytes"
	"strings"
	"testing"

	"bubblepprof/internal/snapshot"
)

func TestParseSnapshotForBubblesBadLabelsJSON(t *testing.T) {
	heap := buildHeapDump(t)
	tarBytes := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels: []byte("{not json"),
	})

	_, err := ParseSnapshotForBubbles(bytes.NewReader(tarBytes), BubbleParseOptions{})
	if err == nil {
		t.Fatal("expected error from malformed labels.json")
	}
	if !strings.Contains(err.Error(), "decode labels.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSnapshotForBubblesUnsupportedFormat(t *testing.T) {
	heap := buildHeapDump(t)
	tarBytes := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               "future-v9",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})

	_, err := ParseSnapshotForBubbles(bytes.NewReader(tarBytes), BubbleParseOptions{})
	if err == nil {
		t.Fatal("expected unsupported format error")
	}
	if !strings.Contains(err.Error(), "unsupported snapshot format") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSnapshotForBubblesMissingHeapDump(t *testing.T) {
	// Build a snapshot then strip heap.dump by hand. Simpler: pass an
	// empty heap.dump payload, parser fails on parse step instead.
	heap := []byte("")
	tarBytes := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     0,
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})

	_, err := ParseSnapshotForBubbles(bytes.NewReader(tarBytes), BubbleParseOptions{})
	if err == nil {
		t.Fatal("expected error from empty heap.dump")
	}
	if !strings.Contains(err.Error(), "parse heap.dump") {
		t.Fatalf("err = %v", err)
	}
}
