package capture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"bubblepprof/internal/snapshot"
)

type fakeHeapWriter struct {
	data []byte
	err  error
}

func (w fakeHeapWriter) WriteHeapDump(fd uintptr) error {
	if w.err != nil {
		return w.err
	}
	f := os.NewFile(fd, "heap")
	if f == nil {
		return errors.New("invalid fd")
	}
	if _, err := f.Write(w.data); err != nil {
		return err
	}
	runtime.KeepAlive(f)
	return nil
}

type fakeProfileWriter struct {
	data []byte
	err  error
}

func (w fakeProfileWriter) WriteGoroutineProfile(out io.Writer) error {
	if w.err != nil {
		return w.err
	}
	_, err := out.Write(w.data)
	return err
}

type fakeMetadataProvider struct {
	got bool
}

func (m *fakeMetadataProvider) Metadata(gcBeforeHeapDump bool) snapshot.SnapshotMetadata {
	m.got = gcBeforeHeapDump
	return snapshot.SnapshotMetadata{GoVersion: "go-fake", PID: 1234}
}

func goodOpts() (CaptureOptions, *bool) {
	gcCalled := false
	return CaptureOptions{
		GCBeforeHeapDump:       true,
		HeapDumpWriter:         fakeHeapWriter{data: []byte("HEAP")},
		GoroutineProfileWriter: fakeProfileWriter{data: []byte("PROFILE")},
		MetadataProvider:       &fakeMetadataProvider{},
		GC:                     func() { gcCalled = true },
	}, &gcCalled
}

func TestCaptureHappyPath(t *testing.T) {
	opts, gcCalled := goodOpts()
	c, err := Capture(context.Background(), opts)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer c.Cleanup()

	if !*gcCalled {
		t.Fatalf("GC should have been called when GCBeforeHeapDump=true")
	}
	if c.HeapDumpSize != int64(len("HEAP")) {
		t.Fatalf("HeapDumpSize = %d", c.HeapDumpSize)
	}
	if string(c.GoroutineProfile) != "PROFILE" {
		t.Fatalf("GoroutineProfile = %q", c.GoroutineProfile)
	}
	// Metadata defaults must be filled in by Capture itself.
	if c.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("Metadata.Format = %q", c.Metadata.Format)
	}
	if c.Metadata.HeapDumpFile != snapshot.HeapDumpFile {
		t.Fatalf("HeapDumpFile = %q", c.Metadata.HeapDumpFile)
	}
	if c.Metadata.GoroutineProfileFile != snapshot.GoroutineProfileFile {
		t.Fatalf("GoroutineProfileFile = %q", c.Metadata.GoroutineProfileFile)
	}
	if !c.Metadata.GCBeforeHeapDump {
		t.Fatalf("GCBeforeHeapDump flag must be propagated to Metadata")
	}

	// HeapDump must be open and positioned at offset 0.
	buf, err := io.ReadAll(c.HeapDump)
	if err != nil {
		t.Fatalf("read heap dump: %v", err)
	}
	if string(buf) != "HEAP" {
		t.Fatalf("heap dump contents = %q", buf)
	}

	// BundleSource hands back the same data.
	src := c.BundleSource()
	if src.HeapDumpSize != c.HeapDumpSize {
		t.Fatalf("BundleSource.HeapDumpSize = %d", src.HeapDumpSize)
	}
}

func TestCaptureSkipsGCWhenDisabled(t *testing.T) {
	opts, gcCalled := goodOpts()
	opts.GCBeforeHeapDump = false
	c, err := Capture(context.Background(), opts)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer c.Cleanup()
	if *gcCalled {
		t.Fatalf("GC should not run when GCBeforeHeapDump=false")
	}
	if c.Metadata.GCBeforeHeapDump {
		t.Fatalf("Metadata.GCBeforeHeapDump should be false")
	}
}

func TestCaptureHeapDumpError(t *testing.T) {
	opts, _ := goodOpts()
	opts.HeapDumpWriter = fakeHeapWriter{err: errors.New("disk full")}
	_, err := Capture(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureProfileError(t *testing.T) {
	opts, _ := goodOpts()
	opts.GoroutineProfileWriter = fakeProfileWriter{err: errors.New("profile boom")}
	_, err := Capture(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "profile boom") {
		t.Fatalf("err = %v", err)
	}
}

// Context that is already canceled at entry must short-circuit, before
// any side effects (no temp dir, no writer invocations).
func TestCaptureCancelledContextUpFront(t *testing.T) {
	opts, gcCalled := goodOpts()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if *gcCalled {
		t.Fatalf("GC must not run after early cancellation")
	}
}

func TestCleanupIsIdempotent(t *testing.T) {
	opts, _ := goodOpts()
	c, err := Capture(context.Background(), opts)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	path := c.HeapDump.Name()
	c.Cleanup()
	c.Cleanup() // must not panic
	var nilC *Captured
	nilC.Cleanup() // also safe

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Cleanup did not remove temp file %q: err=%v", path, err)
	}
}

// WriteSnapshot is the convenience wrapper: it should produce a valid tar
// bundle that round-trips through snapshot.ReadSnapshotBundle.
func TestWriteSnapshotRoundTrip(t *testing.T) {
	opts, _ := goodOpts()
	var buf bytes.Buffer
	if err := WriteSnapshot(context.Background(), &buf, opts); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	bundle, err := snapshot.ReadSnapshotBundle(&buf)
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if string(bundle.HeapDump) != "HEAP" {
		t.Fatalf("heap dump = %q", bundle.HeapDump)
	}
	if string(bundle.GoroutineProfile) != "PROFILE" {
		t.Fatalf("profile = %q", bundle.GoroutineProfile)
	}
	if bundle.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("format = %q", bundle.Metadata.Format)
	}
}

// WriteSnapshot must propagate a Capture-stage error rather than write a
// half-formed bundle.
func TestWriteSnapshotPropagatesCaptureError(t *testing.T) {
	opts, _ := goodOpts()
	opts.HeapDumpWriter = fakeHeapWriter{err: errors.New("explode")}
	var buf bytes.Buffer
	err := WriteSnapshot(context.Background(), &buf, opts)
	if err == nil || !strings.Contains(err.Error(), "explode") {
		t.Fatalf("err = %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("must not write any bytes on capture error; got %d bytes", buf.Len())
	}
}

// withDefaults fills in nil writers and GC with their runtime backends.
func TestWithDefaultsFillsAllSlots(t *testing.T) {
	out := withDefaults(CaptureOptions{})
	if _, ok := out.HeapDumpWriter.(RuntimeHeapDumpWriter); !ok {
		t.Fatalf("HeapDumpWriter default = %T", out.HeapDumpWriter)
	}
	if _, ok := out.GoroutineProfileWriter.(RuntimeGoroutineProfileWriter); !ok {
		t.Fatalf("GoroutineProfileWriter default = %T", out.GoroutineProfileWriter)
	}
	if _, ok := out.MetadataProvider.(RuntimeMetadataProvider); !ok {
		t.Fatalf("MetadataProvider default = %T", out.MetadataProvider)
	}
	if out.GC == nil {
		t.Fatalf("GC must default to a non-nil func")
	}

	// Caller-supplied values must be preserved.
	custom := CaptureOptions{
		HeapDumpWriter:         fakeHeapWriter{},
		GoroutineProfileWriter: fakeProfileWriter{},
		MetadataProvider:       &fakeMetadataProvider{},
		GC:                     func() {},
	}
	preserved := withDefaults(custom)
	if _, ok := preserved.HeapDumpWriter.(fakeHeapWriter); !ok {
		t.Fatalf("withDefaults overwrote HeapDumpWriter: %T", preserved.HeapDumpWriter)
	}
}

// Cancelling the context inside the GC hook (which runs after the
// initial ctx.Err() guard) must trip the second ctx.Err() check inside
// Capture rather than proceed to write the heap dump.
func TestCaptureContextCancelledMidFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	heapHit := false
	opts := CaptureOptions{
		GCBeforeHeapDump: true,
		HeapDumpWriter: fakeHeapWriter{
			data: []byte("HEAP"),
			err:  nil,
		},
		GoroutineProfileWriter: fakeProfileWriter{data: []byte("P")},
		MetadataProvider:       &fakeMetadataProvider{},
		GC:                     func() { cancel() },
	}
	// Wrap heap writer to record whether it was reached.
	opts.HeapDumpWriter = recordingHeapWriter{inner: opts.HeapDumpWriter, hit: &heapHit}

	if _, err := Capture(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if heapHit {
		t.Fatalf("heap dump writer must not run after mid-flight cancel")
	}
}

type recordingHeapWriter struct {
	inner HeapDumpWriter
	hit   *bool
}

func (r recordingHeapWriter) WriteHeapDump(fd uintptr) error {
	*r.hit = true
	return r.inner.WriteHeapDump(fd)
}

// A writer that fails on the very first call surfaces the bundle-write
// error path inside WriteSnapshot.
func TestWriteSnapshotBundleWriteError(t *testing.T) {
	opts, _ := goodOpts()
	err := WriteSnapshot(context.Background(), errWriter{err: errors.New("nope")}, opts)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

type errWriter struct{ err error }

func (e errWriter) Write(_ []byte) (int, error) { return 0, e.err }

// RuntimeGoroutineProfileWriter exercises the standard "goroutine"
// pprof profile to confirm the runtime wiring works end-to-end.
func TestRuntimeGoroutineProfileWriter(t *testing.T) {
	var buf bytes.Buffer
	if err := (RuntimeGoroutineProfileWriter{}).WriteGoroutineProfile(&buf); err != nil {
		t.Fatalf("WriteGoroutineProfile: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty goroutine profile")
	}
}

// RuntimeMetadataProvider must always set the format and file names and
// pass the GC-before-dump flag through.
func TestRuntimeMetadataProvider(t *testing.T) {
	m := RuntimeMetadataProvider{}.Metadata(true)
	if m.Format != snapshot.FormatV1 {
		t.Fatalf("Format = %q", m.Format)
	}
	if m.HeapDumpFile != snapshot.HeapDumpFile || m.GoroutineProfileFile != snapshot.GoroutineProfileFile {
		t.Fatalf("file names = %q / %q", m.HeapDumpFile, m.GoroutineProfileFile)
	}
	if !m.GCBeforeHeapDump {
		t.Fatalf("GCBeforeHeapDump must propagate")
	}
	if m.PID == 0 {
		t.Fatalf("PID should be filled in")
	}
	if m.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should be filled in")
	}
}
