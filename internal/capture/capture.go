package capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"delve_first_project/internal/snapshot"
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

func WriteSnapshot(ctx context.Context, w io.Writer, opts CaptureOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	opts = withDefaults(opts)

	tmpDir, err := os.MkdirTemp("", "bubbleprof-snapshot-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if opts.GCBeforeHeapDump {
		opts.GC()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	heapFile, err := os.CreateTemp(tmpDir, "heap-*.dump")
	if err != nil {
		return fmt.Errorf("create heap dump temp file: %w", err)
	}
	defer heapFile.Close()

	if err := opts.HeapDumpWriter.WriteHeapDump(heapFile.Fd()); err != nil {
		return fmt.Errorf("write heap dump: %w", err)
	}
	if _, err := heapFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind heap dump: %w", err)
	}
	heapInfo, err := heapFile.Stat()
	if err != nil {
		return fmt.Errorf("stat heap dump: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	var goroutines bytes.Buffer
	if err := opts.GoroutineProfileWriter.WriteGoroutineProfile(&goroutines); err != nil {
		return fmt.Errorf("write goroutine profile: %w", err)
	}

	metadata := opts.MetadataProvider.Metadata(opts.GCBeforeHeapDump)
	metadata.Format = snapshot.FormatV1
	metadata.HeapDumpFile = snapshot.HeapDumpFile
	metadata.GoroutineProfileFile = snapshot.GoroutineProfileFile
	metadata.GCBeforeHeapDump = opts.GCBeforeHeapDump

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := snapshot.WriteSnapshotBundle(w, snapshot.BundleSource{
		HeapDump:         heapFile,
		HeapDumpSize:     heapInfo.Size(),
		GoroutineProfile: goroutines.Bytes(),
		Metadata:         metadata,
	}); err != nil {
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
