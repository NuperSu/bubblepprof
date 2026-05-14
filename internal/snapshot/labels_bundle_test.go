package snapshot

import (
	"bytes"
	"testing"
)

func TestWriteReadBundleWithLabelsAndStacks(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:         bytes.NewReader([]byte("HEAP")),
		HeapDumpSize:     int64(len("HEAP")),
		GoroutineProfile: []byte("PROF"),
		Metadata:         testMetadata(),
		Labels:           []byte(`{"format":"bubblepprof-labels-v1"}`),
		GoroutineStacks:  []byte("STACKS"),
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	bundle, err := ReadSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !bytes.Contains(bundle.Labels, []byte("bubblepprof-labels-v1")) {
		t.Fatalf("labels = %q", bundle.Labels)
	}
	if string(bundle.GoroutineStacks) != "STACKS" {
		t.Fatalf("stacks = %q", bundle.GoroutineStacks)
	}

	info, err := InspectSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.HaveLabels || info.LabelsSize == 0 {
		t.Fatalf("HaveLabels=%v size=%d", info.HaveLabels, info.LabelsSize)
	}
	if !info.HaveGoroutineStacks || info.GoroutineStacksSize == 0 {
		t.Fatalf("HaveGoroutineStacks=%v size=%d", info.HaveGoroutineStacks, info.GoroutineStacksSize)
	}
}

func TestReadBundleOldFormatWithoutOptionals(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:         bytes.NewReader([]byte("HEAP")),
		HeapDumpSize:     int64(len("HEAP")),
		GoroutineProfile: []byte("PROF"),
		Metadata:         testMetadata(),
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	bundle, err := ReadSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if bundle.Labels != nil {
		t.Fatalf("Labels = %v", bundle.Labels)
	}
	if bundle.GoroutineStacks != nil {
		t.Fatalf("GoroutineStacks = %v", bundle.GoroutineStacks)
	}

	info, err := InspectSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.HaveLabels || info.HaveGoroutineStacks {
		t.Fatalf("unexpected optionals: labels=%v stacks=%v", info.HaveLabels, info.HaveGoroutineStacks)
	}
}
