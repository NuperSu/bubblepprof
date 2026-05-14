package capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"bubblepprof/internal/snapshot"
)

type HeapDumpWriter interface {
	WriteHeapDump(fd uintptr) error
}

type GoroutineProfileWriter interface {
	WriteGoroutineProfile(w io.Writer) error
}

type MetadataProvider interface {
	Metadata(gcBeforeHeapDump bool) snapshot.SnapshotMetadata
}

type CaptureOptions struct {
	GCBeforeHeapDump bool

	HeapDumpWriter         HeapDumpWriter
	GoroutineProfileWriter GoroutineProfileWriter
	MetadataProvider       MetadataProvider
	GC                     func()
}

// Captured holds the output of Capture: a heap dump open as a temp file
// (positioned at offset 0), the goroutine profile in memory, and the
// metadata. Callers must invoke Cleanup when done.
type Captured struct {
	HeapDump         *os.File
	HeapDumpSize     int64
	GoroutineProfile []byte
	Metadata         snapshot.SnapshotMetadata

	cleanup func()
}

// Cleanup closes the heap dump file and removes the backing temp dir.
// Safe to call more than once.
func (c *Captured) Cleanup() {
	if c == nil || c.cleanup == nil {
		return
	}
	c.cleanup()
	c.cleanup = nil
}

// Capture performs the expensive parts of snapshot capture (heap dump and
// goroutine profile collection) and returns the result without writing it
// out as a tar bundle. Use BundleSource() + snapshot.WriteSnapshotBundle
// to stream the result to a writer.
//
// Splitting capture from bundle-write lets HTTP handlers fail with a
// proper status code before they commit to a 200 response body.
func Capture(ctx context.Context, opts CaptureOptions) (*Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	opts = withDefaults(opts)

	tmpDir, err := os.MkdirTemp("", "bubblepprof-snapshot-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	if opts.GCBeforeHeapDump {
		opts.GC()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	heapFile, err := os.CreateTemp(tmpDir, "heap-*.dump")
	if err != nil {
		return nil, fmt.Errorf("create heap dump temp file: %w", err)
	}

	if err := opts.HeapDumpWriter.WriteHeapDump(heapFile.Fd()); err != nil {
		_ = heapFile.Close()
		return nil, fmt.Errorf("write heap dump: %w", err)
	}
	if _, err := heapFile.Seek(0, io.SeekStart); err != nil {
		_ = heapFile.Close()
		return nil, fmt.Errorf("rewind heap dump: %w", err)
	}
	heapInfo, err := heapFile.Stat()
	if err != nil {
		_ = heapFile.Close()
		return nil, fmt.Errorf("stat heap dump: %w", err)
	}

	if err := ctx.Err(); err != nil {
		_ = heapFile.Close()
		return nil, err
	}

	var goroutines bytes.Buffer
	if err := opts.GoroutineProfileWriter.WriteGoroutineProfile(&goroutines); err != nil {
		_ = heapFile.Close()
		return nil, fmt.Errorf("write goroutine profile: %w", err)
	}

	metadata := opts.MetadataProvider.Metadata(opts.GCBeforeHeapDump)
	metadata.Format = snapshot.FormatV1
	metadata.HeapDumpFile = snapshot.HeapDumpFile
	metadata.GoroutineProfileFile = snapshot.GoroutineProfileFile
	metadata.GCBeforeHeapDump = opts.GCBeforeHeapDump

	c := &Captured{
		HeapDump:         heapFile,
		HeapDumpSize:     heapInfo.Size(),
		GoroutineProfile: goroutines.Bytes(),
		Metadata:         metadata,
	}
	rmTmp := cleanup
	cleanup = nil // success — defer must not delete tmpDir
	c.cleanup = func() {
		_ = heapFile.Close()
		rmTmp()
	}
	return c, nil
}

// BundleSource returns a snapshot.BundleSource pointing at the captured
// data so callers can hand it directly to snapshot.WriteSnapshotBundle.
func (c *Captured) BundleSource() snapshot.BundleSource {
	return snapshot.BundleSource{
		HeapDump:         c.HeapDump,
		HeapDumpSize:     c.HeapDumpSize,
		GoroutineProfile: c.GoroutineProfile,
		Metadata:         c.Metadata,
	}
}

// WriteSnapshot is a convenience that captures and writes the bundle in
// one shot. Prefer Capture + snapshot.WriteSnapshotBundle when you need
// to commit a response status before streaming.
func WriteSnapshot(ctx context.Context, w io.Writer, opts CaptureOptions) error {
	c, err := Capture(ctx, opts)
	if err != nil {
		return err
	}
	defer c.Cleanup()

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := snapshot.WriteSnapshotBundle(w, c.BundleSource()); err != nil {
		return fmt.Errorf("write snapshot bundle: %w", err)
	}
	return nil
}

func withDefaults(opts CaptureOptions) CaptureOptions {
	if opts.HeapDumpWriter == nil {
		opts.HeapDumpWriter = RuntimeHeapDumpWriter{}
	}
	if opts.GoroutineProfileWriter == nil {
		opts.GoroutineProfileWriter = RuntimeGoroutineProfileWriter{}
	}
	if opts.MetadataProvider == nil {
		opts.MetadataProvider = RuntimeMetadataProvider{}
	}
	if opts.GC == nil {
		opts.GC = runtime.GC
	}
	return opts
}
