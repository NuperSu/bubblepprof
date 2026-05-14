package memusage

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshotgraph"
)

// HeapDumpCapturer captures the calling process's heap into a file that
// the parser can stream from. Production uses RuntimeHeapDumpCapturer;
// tests can supply a fake that writes a prerecorded dump.
type HeapDumpCapturer interface {
	CaptureHeapDump(ctx context.Context, gcBefore bool) (path string, cleanup func(), err error)
}

// RuntimeHeapDumpCapturer is the production implementation. It writes
// debug.WriteHeapDump output into a freshly created temp file and seeks
// back to offset 0 so the caller can parse directly.
type RuntimeHeapDumpCapturer struct{}

// CaptureHeapDump implements HeapDumpCapturer.
func (RuntimeHeapDumpCapturer) CaptureHeapDump(ctx context.Context, gcBefore bool) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "bubblepprof-memusage-*.heap")
	if err != nil {
		return "", nil, fmt.Errorf("create heap dump temp file: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	if gcBefore {
		runtime.GC()
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return "", nil, err
	}

	debug.WriteHeapDump(f.Fd())

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("rewind heap dump: %w", err)
	}
	runtime.KeepAlive(f)
	return f.Name(), cleanup, nil
}

// LabelRecoverer is the interface used by Compute to recover heap-native
// pprof labels from a parsed snapshot. The default implementation is
// DefaultLabelRecoverer, which delegates to internal/heaplabels.
type LabelRecoverer interface {
	Recover(snap *heapsnapshot.HeapSnapshot) (heaplabels.Result, error)
}

// DefaultLabelRecoverer recovers labels via internal/heaplabels using the
// runtime.g.labels offset selected from the verified-layouts table.
type DefaultLabelRecoverer struct{}

// Recover implements LabelRecoverer.
func (DefaultLabelRecoverer) Recover(snap *heapsnapshot.HeapSnapshot) (heaplabels.Result, error) {
	if snap == nil {
		return heaplabels.Result{}, fmt.Errorf("memusage: nil heap snapshot")
	}
	offset, ok := heaplabels.LookupGLabelsOffset(snap)
	opts := heaplabels.Options{}
	if ok {
		opts.GLabelsOffset = offset
		opts.HasGLabelsOffset = true
	}
	return heaplabels.DecodeAll(snap, opts), nil
}

// Computer captures, parses, and analyzes a heap dump to answer one
// /debug/memusage request. It is a value with default-zero fields wired
// to production implementations.
type Computer struct {
	Capturer  HeapDumpCapturer
	Recoverer LabelRecoverer
	Opts      Options
}

// NewComputer returns a Computer wired to the runtime implementations.
func NewComputer(opts Options) *Computer {
	return &Computer{
		Capturer:  RuntimeHeapDumpCapturer{},
		Recoverer: DefaultLabelRecoverer{},
		Opts:      opts,
	}
}

// Compute runs the full Phase 1 pipeline:
//
//  1. Capture a heap dump to a temp file.
//  2. Parse it with KeepObjectContents=true (required for heap-native
//     label recovery).
//  3. Recover pprof labels via the configured LabelRecoverer.
//  4. If the runtime layout is unsupported, return before paying the
//     graph-build cost.
//  5. Build the object graph and per-goroutine/global reachability.
//  6. Hand off to ComputeFromAnalysis.
//
// ctx.Err() is checked between stages so a cancelled client does not
// pay for the parse/build work that follows WriteHeapDump (WriteHeapDump
// itself is stop-the-world and cannot be interrupted).
func (c *Computer) Compute(ctx context.Context, req Request) (*Response, error) {
	if c == nil {
		return nil, fmt.Errorf("memusage: nil Computer")
	}
	capturer := c.Capturer
	if capturer == nil {
		capturer = RuntimeHeapDumpCapturer{}
	}
	recoverer := c.Recoverer
	if recoverer == nil {
		recoverer = DefaultLabelRecoverer{}
	}

	path, cleanup, err := capturer.CaptureHeapDump(ctx, c.Opts.GCBeforeHeapDump)
	if err != nil {
		return nil, fmt.Errorf("capture heap dump: %w", err)
	}
	defer cleanup()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open heap dump: %w", err)
	}
	defer f.Close()

	snap, err := heapdump.Parse(f, heapdump.Options{KeepObjectContents: true})
	if err != nil {
		return nil, fmt.Errorf("parse heap dump: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Decode labels first so an unsupported runtime can short-circuit
	// before the (expensive) graph build.
	result, err := recoverer.Recover(snap)
	if err != nil {
		return nil, fmt.Errorf("recover heap-native labels: %w", err)
	}
	diag := DiagnosticsFromHeapLabels(snap, result)
	if diag.UnsupportedRuntime {
		return nil, &UnsupportedRuntimeError{GoVersion: diag.GoVersion, GOARCH: diag.GOARCH}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	analysis, err := snapshotgraph.Build(snap, snapshotgraph.Options{})
	if err != nil {
		return nil, fmt.Errorf("build object graph: %w", err)
	}

	return ComputeFromAnalysis(req, analysis, result.LabelsByGID, diag, c.Opts)
}
