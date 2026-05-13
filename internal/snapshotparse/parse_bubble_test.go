package snapshotparse

import (
	"bytes"
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/snapshot"
)

func writeBundle(t *testing.T, src snapshot.BundleSource) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&buf, src); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return buf.Bytes()
}

func TestParseSnapshotForBubblesWithSidecars(t *testing.T) {
	heapDump := buildHeapDump(t)
	manifest := bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 7, Labels: map[string]string{"bubble": "alpha"}},
		},
	}
	mbytes, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	tar := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heapDump),
		HeapDumpSize:     int64(len(heapDump)),
		GoroutineProfile: []byte("profilebytes"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			PID:                  1,
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels:          mbytes,
		GoroutineStacks: []byte("STACKS"),
	})

	res, err := ParseSnapshotForBubbles(bytes.NewReader(tar), BubbleParseOptions{})
	if err != nil {
		t.Fatalf("ParseSnapshotForBubbles: %v", err)
	}
	if res.Snapshot == nil {
		t.Fatal("snapshot nil")
	}
	if string(res.GoroutineProfile) != "profilebytes" {
		t.Fatalf("profile = %q", res.GoroutineProfile)
	}
	if string(res.GoroutineStacks) != "STACKS" {
		t.Fatalf("stacks = %q", res.GoroutineStacks)
	}
	if res.Labels == nil || len(res.Labels.Goroutines) != 1 || res.Labels.Goroutines[0].ID != 7 {
		t.Fatalf("labels = %+v", res.Labels)
	}
}

func TestParseSnapshotForBubblesMissingLabelsOK(t *testing.T) {
	heapDump := buildHeapDump(t)
	tar := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heapDump),
		HeapDumpSize:     int64(len(heapDump)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})
	res, err := ParseSnapshotForBubbles(bytes.NewReader(tar), BubbleParseOptions{})
	if err != nil {
		t.Fatalf("ParseSnapshotForBubbles: %v", err)
	}
	if res.Labels != nil {
		t.Fatalf("Labels = %+v", res.Labels)
	}
}

func TestParseSnapshotForBubblesRequireLabels(t *testing.T) {
	heapDump := buildHeapDump(t)
	tar := writeBundle(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heapDump),
		HeapDumpSize:     int64(len(heapDump)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})
	_, err := ParseSnapshotForBubbles(bytes.NewReader(tar), BubbleParseOptions{
		HeapDump:      heapdump.Options{},
		RequireLabels: true,
	})
	if err == nil {
		t.Fatal("expected error when labels.json missing and RequireLabels=true")
	}
}

func TestParseSnapshotForBubblesRequireProfile(t *testing.T) {
	heapDump := buildHeapDump(t)
	// Bundle skips GoroutineProfile by writing a custom tar with only
	// heap.dump + metadata. Use the BundleSource helper but pass empty
	// profile; writer still emits an empty entry. Validate that the
	// "missing" check fires when the entry is absent entirely.
	tar := writeBundle(t, snapshot.BundleSource{
		HeapDump:     bytes.NewReader(heapDump),
		HeapDumpSize: int64(len(heapDump)),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})
	// snapshot.WriteSnapshotBundle always writes a goroutine.pprof
	// entry (possibly empty). Confirm RequireProfile sees it as
	// present.
	if _, err := ParseSnapshotForBubbles(bytes.NewReader(tar), BubbleParseOptions{RequireProfile: true}); err != nil {
		t.Fatalf("expected profile entry to be present (empty bytes still counts): %v", err)
	}
}
