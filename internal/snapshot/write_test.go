package snapshot

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriteSnapshotBundleRejectsNilReader(t *testing.T) {
	var buf bytes.Buffer
	err := WriteSnapshotBundle(&buf, BundleSource{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "heap dump reader is nil") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteSnapshotBundleRejectsNegativeSize(t *testing.T) {
	var buf bytes.Buffer
	err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:     strings.NewReader("data"),
		HeapDumpSize: -1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Fatalf("err = %v", err)
	}
}

// failingWriter returns an error after writing some bytes — used to
// exercise the tar-writer error paths.
type failingWriter struct {
	limit int
	wrote int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.wrote >= w.limit {
		return 0, errors.New("simulated write failure")
	}
	if w.wrote+len(p) > w.limit {
		n := w.limit - w.wrote
		w.wrote = w.limit
		return n, errors.New("simulated write failure")
	}
	w.wrote += len(p)
	return len(p), nil
}

func TestWriteSnapshotBundlePropagatesWriteError(t *testing.T) {
	w := &failingWriter{limit: 10}
	err := WriteSnapshotBundle(w, BundleSource{
		HeapDump:         strings.NewReader("FAKE_HEAP"),
		HeapDumpSize:     int64(len("FAKE_HEAP")),
		GoroutineProfile: []byte("P"),
		Metadata:         testMetadata(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteSnapshotBundleWithLabelsAndStacks(t *testing.T) {
	var buf bytes.Buffer
	err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:         strings.NewReader("h"),
		HeapDumpSize:     1,
		GoroutineProfile: []byte("p"),
		Metadata:         testMetadata(),
		Labels:           []byte("{}"),
		GoroutineStacks:  []byte("stack"),
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := InspectSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.HaveLabels {
		t.Fatal("HaveLabels = false")
	}
	if !info.HaveGoroutineStacks {
		t.Fatal("HaveGoroutineStacks = false")
	}
	if info.LabelsSize != 2 || info.GoroutineStacksSize != int64(len("stack")) {
		t.Fatalf("sizes: labels=%d stacks=%d", info.LabelsSize, info.GoroutineStacksSize)
	}
}
