package capture

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
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

func goodOpts() (CaptureOptions, *bool) {
	gcCalled := false
	return CaptureOptions{
		GCBeforeHeapDump: true,
		HeapDumpWriter:   fakeHeapWriter{data: []byte("HEAP")},
		GC:               func() { gcCalled = true },
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

	buf, err := io.ReadAll(c.HeapDump)
	if err != nil {
		t.Fatalf("read heap dump: %v", err)
	}
	if string(buf) != "HEAP" {
		t.Fatalf("heap dump contents = %q", buf)
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
}

func TestCaptureHeapDumpError(t *testing.T) {
	opts, _ := goodOpts()
	opts.HeapDumpWriter = fakeHeapWriter{err: errors.New("disk full")}
	_, err := Capture(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

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
	c.Cleanup()
	var nilC *Captured
	nilC.Cleanup()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Cleanup did not remove temp file %q: err=%v", path, err)
	}
}

func TestCaptureContextCancelledMidFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	heapHit := false
	opts := CaptureOptions{
		GCBeforeHeapDump: true,
		HeapDumpWriter:   recordingHeapWriter{inner: fakeHeapWriter{data: []byte("HEAP")}, hit: &heapHit},
		GC:               func() { cancel() },
	}

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

func TestWithDefaultsFillsAllSlots(t *testing.T) {
	out := withDefaults(CaptureOptions{})
	if _, ok := out.HeapDumpWriter.(RuntimeHeapDumpWriter); !ok {
		t.Fatalf("HeapDumpWriter default = %T", out.HeapDumpWriter)
	}
	if out.GC == nil {
		t.Fatalf("GC must default to a non-nil func")
	}

	custom := CaptureOptions{
		HeapDumpWriter: fakeHeapWriter{},
		GC:             func() {},
	}
	preserved := withDefaults(custom)
	if _, ok := preserved.HeapDumpWriter.(fakeHeapWriter); !ok {
		t.Fatalf("withDefaults overwrote HeapDumpWriter: %T", preserved.HeapDumpWriter)
	}
}
