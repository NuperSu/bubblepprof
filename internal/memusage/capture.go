package memusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sync"

	"bubblepprof/internal/addrspace"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/runtimelayout"
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
//
// extra is an optional addrspace.Reader (typically an
// addrspace.ProcessReader on Linux for /debug/memusage) consulted when
// label string bytes are not present in heap object contents. nil
// disables the fallback.
type LabelRecoverer interface {
	Recover(snap *heapsnapshot.HeapSnapshot, extra addrspace.Reader) (heaplabels.Result, error)
}

// DefaultLabelRecoverer recovers labels via internal/heaplabels using the
// runtime layout chosen by internal/runtimelayout. When the runtime
// layout is unsupported, every goroutine is reported with
// StatusUnsupportedRuntime so the compute layer can short-circuit before
// the expensive graph build.
type DefaultLabelRecoverer struct{}

// Recover implements LabelRecoverer.
func (DefaultLabelRecoverer) Recover(snap *heapsnapshot.HeapSnapshot, extra addrspace.Reader) (heaplabels.Result, error) {
	if snap == nil {
		return heaplabels.Result{}, fmt.Errorf("memusage: nil heap snapshot")
	}
	input := heaplabels.LookupInputFromSnapshot(snap)
	layout, ok := runtimelayout.Lookup(input)
	if !ok {
		return heaplabels.UnsupportedResult(snap, runtimelayout.UnsupportedMessage(input)), nil
	}
	return heaplabels.DecodeAll(snap, layout, heaplabels.Options{ExtraStringMemory: extra}), nil
}

// Computer captures, parses, and analyzes a heap dump to answer one
// /debug/memusage request. It is a value with default-zero fields wired
// to production implementations.
//
// The process memory reader (/proc/self/mem on Linux) is opened lazily on
// the first Compute call and reused across subsequent requests. Call Close
// when the Computer is no longer needed to release the underlying file
// descriptor. Close must not be called concurrently with Compute.
type Computer struct {
	Capturer  HeapDumpCapturer
	Recoverer LabelRecoverer
	Opts      Options

	procOnce    sync.Once
	procReader  *addrspace.ProcessReader
	procWarning string
}

// NewComputer returns a Computer wired to the runtime implementations.
func NewComputer(opts Options) *Computer {
	return &Computer{
		Capturer:  RuntimeHeapDumpCapturer{},
		Recoverer: DefaultLabelRecoverer{},
		Opts:      opts,
	}
}

// Close releases the cached process memory reader. Safe to call multiple
// times. Computer must not be used after Close returns.
func (c *Computer) Close() error {
	if c == nil || c.procReader == nil {
		return nil
	}
	return c.procReader.Close()
}

// processReader returns the cached process reader, opening it lazily on
// first call. The returned reader must not be closed by the caller.
func (c *Computer) processReader() (*addrspace.ProcessReader, string) {
	c.procOnce.Do(func() {
		c.procReader, c.procWarning = openProcessReader(c.Opts.DisableProcessMemoryReader)
	})
	return c.procReader, c.procWarning
}

// Compute runs the full /debug/memusage pipeline:
//
//  1. Capture a heap dump to a temp file.
//  2. Parse it with KeepObjectContents=true (required for heap-native
//     label recovery).
//  3. Open an in-process address-space reader (Linux only; gated by
//     Opts.DisableProcessMemoryReader) so the heap-label decoder can
//     recover ordinary runtime/pprof string literals that live outside
//     heap object contents.
//  4. Resolve the runtime layout via runtimelayout.Lookup and recover
//     pprof labels via the configured LabelRecoverer.
//  5. If the runtime layout is unsupported, return UnsupportedRuntimeError
//     before paying the graph-build cost.
//  6. Build the object graph (structural; no reachability) and hand off
//     to ComputeFromAnalysis, which performs the single reachability
//     pass from matched roots.
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
		return nil, &CaptureFailedError{Cause: err}
	}
	defer cleanup()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, &CaptureFailedError{Cause: err}
	}
	defer f.Close()

	snap, err := heapdump.Parse(f, heapdump.Options{KeepObjectContents: true})
	if err != nil {
		return nil, &ParseFailedError{Cause: err}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	procReader, procWarning := c.processReader()

	// Decode labels first so an unsupported runtime can short-circuit
	// before the (expensive) graph build.
	var extra addrspace.Reader
	if procReader != nil {
		extra = procReader
	}
	result, err := recoverer.Recover(snap, extra)
	if err != nil {
		return nil, fmt.Errorf("recover heap-native labels: %w", err)
	}
	diag := DiagnosticsFromHeapLabels(snap, result)
	if procWarning != "" {
		diag.Warnings = append(diag.Warnings, procWarning)
	}
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

// openProcessReader tries to open the in-process address-space reader
// and returns a warning string describing the outcome when the reader
// is not available. Callers must Close a non-nil reader.
//
// The warning string follows the literal phrasing required by Phase 3
// so /debug/memusage clients can detect literal-label limitations from
// the response alone. An empty warning means the reader is available.
func openProcessReader(disabled bool) (*addrspace.ProcessReader, string) {
	if disabled {
		return nil, "process memory reader disabled by options; literal pprof label strings may be unrecoverable"
	}
	r, err := addrspace.OpenSelfProcessReader()
	if err != nil {
		if errors.Is(err, addrspace.ErrUnsupported) {
			return nil, "process memory reader unavailable on this platform; literal pprof label strings may be unrecoverable"
		}
		return nil, fmt.Sprintf("process memory reader unavailable: %v; literal pprof label strings may be unrecoverable", err)
	}
	return r, ""
}
