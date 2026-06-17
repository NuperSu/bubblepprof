package memusage

import (
	"context"
	"fmt"
	"io"
	"runtime/trace"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heapdump"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

// AnalyzeDump runs the analyse side of the /debug/memusage pipeline
// against an already-captured heap dump: parse, heap-native label
// recovery, graph build, and the single reachability pass from matched
// roots. It is the shared core behind both the in-process endpoint
// ((*Computer).Compute) and the external analyser (bundle + CLI), so
// error codes and diagnostics are identical in both modes.
//
// r and ra must view the same dump bytes: r is streamed by the parser
// while ra serves lazy object-content reads, and both must remain valid
// until AnalyzeDump returns (an *os.File satisfies both).
//
// extra is the optional reader for label string bytes that live outside
// heap object contents (.rodata literals). In-process this is an
// addrspace.ProcessReader; offline it is a reader over the bundle's
// saved read-only segments. nil disables the fallback, in which case
// literal labels may fail with StringMissingError — identical to the
// DisableProcessMemoryReader path.
//
// extraWarnings are appended to the response diagnostics (e.g. the
// "process memory reader unavailable..." phrasing when no extra reader
// could be provided).
func AnalyzeDump(
	ctx context.Context,
	r io.Reader,
	ra io.ReaderAt,
	extra addrspace.Reader,
	extraWarnings []string,
	req Request,
	opts Options,
) (*Response, error) {
	return analyzeDump(ctx, r, ra, DefaultLabelRecoverer{}, extra, extraWarnings, req, opts)
}

// analyzeDump is AnalyzeDump with an injectable LabelRecoverer so
// (*Computer).Compute keeps its test seam.
func analyzeDump(
	ctx context.Context,
	r io.Reader,
	ra io.ReaderAt,
	recoverer LabelRecoverer,
	extra addrspace.Reader,
	extraWarnings []string,
	req Request,
	opts Options,
) (*Response, error) {
	if verr := ValidateRequest(&req, opts); verr != nil {
		return nil, verr
	}
	if recoverer == nil {
		recoverer = DefaultLabelRecoverer{}
	}

	snap, result, diag, err := parseAndRecoverLabels(ctx, r, ra, recoverer, extra, extraWarnings)
	if err != nil {
		return nil, err
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

	// Past this point only the graph is consulted; drop the per-object
	// PointerAddrs slices so ComputeFromAnalysis runs against the graph
	// alone.
	for i := range snap.Objects {
		snap.Objects[i].PointerAddrs = nil
	}
	snap = nil

	computeRegion := trace.StartRegion(ctx, "memusage/compute_from_analysis")
	resp, err := ComputeFromAnalysis(req, analysis, result.LabelsByGID, diag, opts)
	computeRegion.End()
	return resp, err
}

func parseAndRecoverLabels(
	ctx context.Context,
	r io.Reader,
	ra io.ReaderAt,
	recoverer LabelRecoverer,
	extra addrspace.Reader,
	extraWarnings []string,
) (*heapsnapshot.HeapSnapshot, heaplabels.Result, Diagnostics, error) {
	if recoverer == nil {
		recoverer = DefaultLabelRecoverer{}
	}
	parseRegion := trace.StartRegion(ctx, "memusage/parse")
	snap, resolver, err := heapdump.ParseLazyContents(r, ra, heapdump.Options{Strict: true})
	parseRegion.End()
	if err != nil {
		return nil, heaplabels.Result{}, Diagnostics{}, &ParseFailedError{Cause: err}
	}

	if err := ctx.Err(); err != nil {
		return nil, heaplabels.Result{}, Diagnostics{}, err
	}

	// Decode labels first so an unsupported runtime can short-circuit
	// before the (expensive) graph build.
	//
	// Back the structural read Memory with the lazy resolver so we do
	// not retain ~the workload heap worth of object content bytes in
	// the Go heap. The decoder fetches bytes from ra on demand.
	heapMem := heaplabels.NewMemoryFromReader(resolver)
	labelsRegion := trace.StartRegion(ctx, "memusage/labels")
	result, err := recoverer.Recover(snap, heapMem, extra)
	labelsRegion.End()
	if err != nil {
		return nil, heaplabels.Result{}, Diagnostics{}, fmt.Errorf("recover heap-native labels: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, heaplabels.Result{}, Diagnostics{}, err
	}
	diag := DiagnosticsFromHeapLabels(snap, result)
	diag.Warnings = append(diag.Warnings, extraWarnings...)
	return snap, result, diag, nil
}
