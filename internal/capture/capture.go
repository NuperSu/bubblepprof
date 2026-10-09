// Package capture writes the calling process's heap dump to a temp file
// and returns it positioned at offset 0. It is used by dev tools and
// tests that need a live heap dump without the snapshot-tar bundle format.
package capture

import (
	"context"
	"fmt"
	"github.com/NuperSu/bubblepprof/internal/diskspace"
	"io"
	"os"
	"runtime"
	"runtime/trace"
)

// HeapDumpWriter writes the calling process heap dump to the file
// identified by fd.
type HeapDumpWriter interface {
	WriteHeapDump(fd uintptr) error
}

// CaptureOptions configures Capture. The zero value is useful: it
// selects RuntimeHeapDumpWriter and skips the GC.
type CaptureOptions struct {
	// GCBeforeHeapDump runs runtime.GC() before WriteHeapDump.
	GCBeforeHeapDump bool
	// ScratchDir defaults to the system temporary directory.
	ScratchDir string
	// MinFreeBytes defaults to a modest 64 MiB preflight on supported systems.
	MinFreeBytes uint64
	// MaxHeapDumpBytes is checked after WriteHeapDump (0 is unlimited).
	MaxHeapDumpBytes int64
	// CheckSpace can replace the production preflight in internal tests.
	CheckSpace func(string, uint64) error

	// HeapDumpWriter writes the heap dump. Defaults to
	// RuntimeHeapDumpWriter when nil.
	HeapDumpWriter HeapDumpWriter

	// GC is the GC trigger. Defaults to runtime.GC when nil.
	GC func()
}

// Captured holds a heap dump open as a seeked-to-zero temp file.
// Call Cleanup when done.
type Captured struct {
	HeapDump     *os.File
	HeapDumpSize int64

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

// Capture writes a heap dump to a temp file and returns it positioned
// at offset 0. The caller must invoke Cleanup when done.
func Capture(ctx context.Context, opts CaptureOptions) (*Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if opts.MaxHeapDumpBytes < 0 {
		return nil, fmt.Errorf("maximum heap dump bytes must be non-negative")
	}
	opts = withDefaults(opts)

	tmpDir, err := os.MkdirTemp(opts.ScratchDir, "bubblepprof-capture-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	if err := opts.CheckSpace(tmpDir, opts.MinFreeBytes); err != nil {
		return nil, err
	}
	if opts.GCBeforeHeapDump {
		region := trace.StartRegion(ctx, "memusage/gc_pre")
		opts.GC()
		region.End()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	heapFile, err := os.CreateTemp(tmpDir, "heap-*.dump")
	if err != nil {
		return nil, fmt.Errorf("create heap dump temp file: %w", err)
	}

	region := trace.StartRegion(ctx, "memusage/write_heap_dump")
	writeErr := opts.HeapDumpWriter.WriteHeapDump(heapFile.Fd())
	region.End()
	if err := writeErr; err != nil {
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
		heapFile.Close()
		return nil, err
	}
	if opts.MaxHeapDumpBytes > 0 && heapInfo.Size() > opts.MaxHeapDumpBytes {
		heapFile.Close()
		return nil, fmt.Errorf("heap dump size %d exceeds limit %d", heapInfo.Size(), opts.MaxHeapDumpBytes)
	}
	c := &Captured{
		HeapDump:     heapFile,
		HeapDumpSize: heapInfo.Size(),
	}
	rmTmp := cleanup
	cleanup = nil
	c.cleanup = func() {
		_ = heapFile.Close()
		rmTmp()
	}
	return c, nil
}

func withDefaults(opts CaptureOptions) CaptureOptions {
	if opts.HeapDumpWriter == nil {
		opts.HeapDumpWriter = RuntimeHeapDumpWriter{}
	}
	if opts.CheckSpace == nil {
		opts.CheckSpace = diskspace.Check
	}
	if opts.GC == nil {
		opts.GC = runtime.GC
	}
	return opts
}
