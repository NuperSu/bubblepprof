package capture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/snapshot"
)

type fakeStacksWriter struct {
	data []byte
	err  error
}

func (f fakeStacksWriter) WriteGoroutineStacks(w io.Writer) error {
	if f.err != nil {
		return f.err
	}
	_, err := w.Write(f.data)
	return err
}

type fakeLabelProvider struct {
	bytes []byte
	err   error
}

func (f fakeLabelProvider) LabelManifest() ([]byte, error) {
	return f.bytes, f.err
}

func TestCaptureIncludesLabelsAndStacks(t *testing.T) {
	opts, _ := goodOpts()
	opts.LabelManifestProvider = fakeLabelProvider{bytes: []byte(`{"format":"bubblepprof-labels-v1"}`)}
	opts.GoroutineStacksWriter = fakeStacksWriter{data: []byte("STACKS")}

	var buf bytes.Buffer
	if err := WriteSnapshot(context.Background(), &buf, opts); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	b, err := snapshot.ReadSnapshotBundle(&buf)
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if !bytes.Contains(b.Labels, []byte(`bubblepprof-labels-v1`)) {
		t.Fatalf("Labels = %q", b.Labels)
	}
	if string(b.GoroutineStacks) != "STACKS" {
		t.Fatalf("GoroutineStacks = %q", b.GoroutineStacks)
	}
	if b.Metadata.LabelsFile != snapshot.LabelsFile {
		t.Fatalf("Metadata.LabelsFile = %q", b.Metadata.LabelsFile)
	}
	if b.Metadata.GoroutineStacksFile != snapshot.GoroutineStacksFile {
		t.Fatalf("Metadata.GoroutineStacksFile = %q", b.Metadata.GoroutineStacksFile)
	}
}

func TestCaptureOmitsEmptyLabels(t *testing.T) {
	opts, _ := goodOpts()
	opts.LabelManifestProvider = fakeLabelProvider{bytes: nil}
	opts.GoroutineStacksWriter = fakeStacksWriter{data: nil}

	var buf bytes.Buffer
	if err := WriteSnapshot(context.Background(), &buf, opts); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	b, err := snapshot.ReadSnapshotBundle(&buf)
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if b.Labels != nil {
		t.Fatalf("Labels = %q", b.Labels)
	}
	if b.GoroutineStacks != nil {
		t.Fatalf("GoroutineStacks = %q", b.GoroutineStacks)
	}
	if b.Metadata.LabelsFile != "" {
		t.Fatalf("Metadata.LabelsFile = %q", b.Metadata.LabelsFile)
	}
	if b.Metadata.GoroutineStacksFile != "" {
		t.Fatalf("Metadata.GoroutineStacksFile = %q", b.Metadata.GoroutineStacksFile)
	}
}

func TestCaptureLabelProviderError(t *testing.T) {
	opts, _ := goodOpts()
	opts.LabelManifestProvider = fakeLabelProvider{err: errors.New("boom")}
	_, err := Capture(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureStacksWriterError(t *testing.T) {
	opts, _ := goodOpts()
	opts.GoroutineStacksWriter = fakeStacksWriter{err: errors.New("stacks fail")}
	_, err := Capture(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "stacks fail") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryLabelManifestProviderEmpty(t *testing.T) {
	p := RegistryLabelManifestProvider{Registry: bubblelabels.NewRegistry()}
	got, err := p.LabelManifest()
	if err != nil {
		t.Fatalf("LabelManifest: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil bytes for empty registry, got %d bytes", len(got))
	}
}

func TestRegistryLabelManifestProviderNonEmpty(t *testing.T) {
	r := bubblelabels.NewRegistry()
	r.Set(7, map[string]string{"bubble": "alpha"})
	p := RegistryLabelManifestProvider{Registry: r, Source: "bubblepprof.Do"}
	got, err := p.LabelManifest()
	if err != nil {
		t.Fatalf("LabelManifest: %v", err)
	}
	if !bytes.Contains(got, []byte("bubble")) {
		t.Fatalf("manifest payload = %s", got)
	}
	m, err := bubblelabels.DecodeManifest(got)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if len(m.Goroutines) != 1 || m.Goroutines[0].ID != 7 {
		t.Fatalf("decoded manifest = %+v", m)
	}
	if m.Goroutines[0].Source != "bubblepprof.Do" {
		t.Fatalf("source = %q", m.Goroutines[0].Source)
	}
	if m.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should be set")
	}
	_ = time.Now
}

func TestRuntimeGoroutineStacksWriter(t *testing.T) {
	var buf bytes.Buffer
	if err := (RuntimeGoroutineStacksWriter{}).WriteGoroutineStacks(&buf); err != nil {
		t.Fatalf("WriteGoroutineStacks: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty debug=2 stacks")
	}
	if !bytes.Contains(buf.Bytes(), []byte("goroutine ")) {
		t.Fatalf("debug=2 dump did not include header: %s", buf.String())
	}
}
