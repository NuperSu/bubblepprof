package memusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/trace"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heapdump"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
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
		region := trace.StartRegion(ctx, "memusage/gc_pre")
		runtime.GC()
		region.End()
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return "", nil, err
	}

	region := trace.StartRegion(ctx, "memusage/write_heap_dump")
	debug.WriteHeapDump(f.Fd())
	region.End()

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
// mem is an optional pre-built heap-memory source. When non-nil, the
// recoverer delegates structural reads to it (used by the lazy parse
// path so structural reads go through a heapdump.ContentResolver
// instead of materialized object content bytes). Pass nil to let the
// recoverer build an eager Memory from snap.Objects[i].Contents.
//
// extra is an optional addrspace.Reader (typically an
// addrspace.ProcessReader on Linux/Darwin for /debug/memusage) consulted when
// label string bytes are not present in heap object contents. nil
// disables the fallback.
type LabelRecoverer interface {
	Recover(snap *heapsnapshot.HeapSnapshot, mem *heaplabels.Memory, extra addrspace.Reader) (heaplabels.Result, error)
}

// DefaultLabelRecoverer recovers labels via internal/heaplabels using the
// runtime layout chosen by internal/runtimelayout. When the runtime
// layout is unsupported, every goroutine is reported with
// StatusUnsupportedRuntime so the compute layer can short-circuit before
// the expensive graph build.
type DefaultLabelRecoverer struct{}

// Recover implements LabelRecoverer.
func (r DefaultLabelRecoverer) Recover(snap *heapsnapshot.HeapSnapshot, mem *heaplabels.Memory, extra addrspace.Reader) (heaplabels.Result, error) {
	if snap == nil {
		return heaplabels.Result{}, fmt.Errorf("memusage: nil heap snapshot")
	}
	input := heaplabels.LookupInputFromSnapshot(snap)
	layout, ok := runtimelayout.Lookup(input)
	if !ok {
		return heaplabels.UnsupportedResult(snap, runtimelayout.UnsupportedMessage(input)), nil
	}
	return heaplabels.DecodeAll(snap, layout, heaplabels.Options{
		ExtraStringMemory: extra,
		HeapMemory:        mem,
	}), nil
}

// Computer captures, parses, and analyzes a heap dump to answer one
// /debug/memusage request. It is a value with default-zero fields wired
// to production implementations.
//
// The process memory reader is opened at the start of each Compute call
// and closed before it returns; on Linux this re-reads /proc/self/maps
// per request, so mappings added after startup (dlopen, plugin.Open) are
// visible. Sources by platform:
//
//   - Linux: /proc/self/mem.
//   - FreeBSD: /proc/self/mem when procfs is mounted; otherwise the on-disk
//     ELF executable (correct only for non-PIE binaries).
//   - Darwin: Mach-O segments of the running executable with ASLR slide
//     correction.
//   - Windows: PE sections of the running executable with ASLR slide
//     correction.
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

// Close is a no-op retained for backward compatibility: Compute opens
// and closes its process memory reader per request and the Computer
// holds no other resources. Safe to call multiple times.
func (c *Computer) Close() error {
	return nil
}

// Compute runs the full /debug/memusage pipeline:
//
//  1. Capture a heap dump to a temp file.
//  2. Parse it with ParseLazyContents so object content bytes are not
//     retained in the Go heap; instead a ContentResolver fetches them
//     from the dump file on demand during label recovery.
//  3. Open an in-process address-space reader (Linux, macOS, FreeBSD,
//     Windows; gated by Opts.DisableProcessMemoryReader) so the heap-label
//     decoder can recover ordinary runtime/pprof string literals that live
//     outside heap object contents. On FreeBSD the reader requires procfs
//     mounted at /proc or a non-PIE binary; if neither condition holds,
//     literal labels surface as string_missing.
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
	// Validate and pre-flight before the stop-the-world dump: an invalid
	// request or an unsupported local runtime must not pay for capture and
	// parse. The dump's own params remain the authoritative lookup key —
	// ComputeFromAnalysis re-checks diag.UnsupportedRuntime after decode.
	if verr := ValidateRequest(&req, c.Opts); verr != nil {
		return nil, verr
	}
	localInput := runtimelayout.LocalInput()
	if _, ok := runtimelayout.Lookup(localInput); !ok {
		return nil, &UnsupportedRuntimeError{GoVersion: localInput.GoVersion, GOARCH: localInput.GOARCH}
	}
	capturer := c.Capturer
	if capturer == nil {
		capturer = RuntimeHeapDumpCapturer{}
	}
	recoverer := c.Recoverer
	if recoverer == nil {
		recoverer = DefaultLabelRecoverer{}
	}

	captureRegion := trace.StartRegion(ctx, "memusage/capture")
	path, cleanup, err := capturer.CaptureHeapDump(ctx, c.Opts.GCBeforeHeapDump)
	captureRegion.End()
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

	parseRegion := trace.StartRegion(ctx, "memusage/parse")
	snap, resolver, err := heapdump.ParseLazyContents(f, f, heapdump.Options{Strict: true})
	parseRegion.End()
	if err != nil {
		return nil, &ParseFailedError{Cause: err}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	procReader, procWarning := openProcessReader(c.Opts.DisableProcessMemoryReader)
	if procReader != nil {
		defer procReader.Close()
	}

	// Decode labels first so an unsupported runtime can short-circuit
	// before the (expensive) graph build.
	var extra addrspace.Reader
	if procReader != nil {
		extra = procReader
	}
	// Back the structural read Memory with the lazy resolver so we do
	// not retain ~the workload heap worth of object content bytes in
	// the Go heap. The decoder fetches bytes from f on demand via
	// io.ReaderAt; f stays open until Compute returns (defer below).
	heapMem := heaplabels.NewMemoryFromReader(resolver)
	labelsRegion := trace.StartRegion(ctx, "memusage/labels")
	result, err := recoverer.Recover(snap, heapMem, extra)
	labelsRegion.End()
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

	// Drop per-object content bytes now that label recovery has consumed
	// them. snapshotgraph.Build never reads Contents — only PointerAddrs,
	// Addr, Size. Nilling out here lets the GC reclaim ~the workload's
	// heap worth of bytes before Build allocates the graph.
	for i := range snap.Objects {
		snap.Objects[i].Contents = nil
	}

	buildRegion := trace.StartRegion(ctx, "memusage/build")
	analysis, err := snapshotgraph.Build(snap, snapshotgraph.Options{})
	buildRegion.End()
	if err != nil {
		return nil, fmt.Errorf("build object graph: %w", err)
	}

	// Build no longer needs the per-object PointerAddrs slices. Drop them
	// so ComputeFromAnalysis runs against the graph alone.
	for i := range snap.Objects {
		snap.Objects[i].PointerAddrs = nil
	}
	snap = nil

	computeRegion := trace.StartRegion(ctx, "memusage/compute_from_analysis")
	resp, err := ComputeFromAnalysis(req, analysis, result.LabelsByGID, diag, c.Opts)
	computeRegion.End()
	return resp, err
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
