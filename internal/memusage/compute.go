package memusage

import (
	"fmt"

	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshotgraph"
)

// LabelsMatch reports whether the goroutine's recovered labels (have)
// contain every key/value pair in the selector (want). Extra labels on
// the goroutine are allowed.
func LabelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		hv, ok := have[k]
		if !ok || hv != v {
			return false
		}
	}
	return true
}

// UnionReachable unions the reachability sets of the supplied goroutines
// into a single ObjectID set. Counted-once semantics: an object reached
// by multiple goroutines appears exactly once in the result.
func UnionReachable(goroutines []snapshotgraph.GoroutineReachability) map[snapshotgraph.ObjectID]struct{} {
	out := make(map[snapshotgraph.ObjectID]struct{})
	for _, gr := range goroutines {
		for id := range gr.Reachable {
			out[id] = struct{}{}
		}
	}
	return out
}

// ObjectSetBytes sums the shallow size of the objects in set, skipping
// IDs that are out of range (defensive; a normal Build never produces
// such IDs).
func ObjectSetBytes(g *snapshotgraph.Graph, set map[snapshotgraph.ObjectID]struct{}) uint64 {
	if g == nil {
		return 0
	}
	var n uint64
	for id := range set {
		if int(id) < len(g.Objects) {
			n += g.Objects[id].Size
		}
	}
	return n
}

// IntersectCountBytes returns the size of a∩b and the sum of shallow
// sizes of objects in that intersection.
func IntersectCountBytes(g *snapshotgraph.Graph, a, b map[snapshotgraph.ObjectID]struct{}) (int, uint64) {
	if g == nil || len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	if len(b) < len(a) {
		a, b = b, a
	}
	var (
		count int
		bytes uint64
	)
	for id := range a {
		if _, ok := b[id]; !ok {
			continue
		}
		count++
		if int(id) < len(g.Objects) {
			bytes += g.Objects[id].Size
		}
	}
	return count, bytes
}

// Diagnostics summarizes label-recovery state needed by ComputeFromAnalysis
// to decide attribution and surface honest warnings/errors.
type Diagnostics struct {
	GoVersion string
	GOARCH    string

	// UnsupportedRuntime is true when the heap-native label decoder
	// could not locate runtime.g.labels for this dump, e.g. because the
	// Go version/GOARCH is not in the verified layout table.
	UnsupportedRuntime bool

	// StringMissingCount is the number of goroutines whose label decoding
	// failed because the string bytes (key or value) were not preserved
	// in heap dump object contents. Common for literal pprof.Labels.
	StringMissingCount int

	// FailedGoroutines is the number of goroutines whose label decode
	// failed for any reason (excluding StatusDecoded / StatusNoLabels /
	// StatusUnsupportedRuntime).
	FailedGoroutines int

	Warnings []string
}

// UnsupportedRuntimeError is returned by ComputeFromAnalysis when label
// recovery is structurally impossible on the current runtime layout. The
// handler turns this into HTTP 422 with code "unsupported_runtime".
type UnsupportedRuntimeError struct {
	GoVersion string
	GOARCH    string
}

func (e *UnsupportedRuntimeError) Error() string {
	return fmt.Sprintf("heap-native pprof label recovery is unsupported for this Go runtime (%s %s)", e.GoVersion, e.GOARCH)
}

// StringMissingError is returned by ComputeFromAnalysis when no requested
// labels could be matched because every candidate goroutine's labels
// failed to decode with status string_missing. The handler turns this
// into HTTP 422 with code "string_missing".
type StringMissingError struct {
	GoVersion string
	GOARCH    string
	Warnings  []string
}

func (e *StringMissingError) Error() string {
	return "pprof labels were found but some label string bytes were unavailable"
}

// ComputeFromAnalysis is the pure core of /debug/memusage: it takes an
// already-built object graph, a precomputed labelsByGID map, and label
// diagnostics, and returns the response payload.
func ComputeFromAnalysis(
	req Request,
	analysis *snapshotgraph.Analysis,
	labelsByGID map[uint64]map[string]string,
	diag Diagnostics,
	opts Options,
) (*Response, error) {
	if analysis == nil {
		return nil, fmt.Errorf("memusage: analysis is nil")
	}
	if analysis.Graph == nil {
		return nil, fmt.Errorf("memusage: analysis has no graph")
	}
	if diag.UnsupportedRuntime {
		return nil, &UnsupportedRuntimeError{GoVersion: diag.GoVersion, GOARCH: diag.GOARCH}
	}

	g := analysis.Graph

	matched := make([]snapshotgraph.GoroutineReachability, 0)
	var systemGoroutines []snapshotgraph.GoroutineReachability
	for _, gr := range analysis.Goroutines {
		isSystem := gr.IsSystem || gr.IsBackground
		if isSystem {
			systemGoroutines = append(systemGoroutines, gr)
			if !opts.IncludeSystemGoroutines {
				continue
			}
		}
		have := labelsByGID[gr.GoroutineID]
		if !LabelsMatch(have, req.Labels) {
			continue
		}
		matched = append(matched, gr)
	}

	// String-missing all the way down? If no labels at all could be
	// decoded and at least one decode attempt failed with string_missing,
	// surface a clear incomplete error.
	if len(matched) == 0 && diag.StringMissingCount > 0 && len(labelsByGID) == 0 {
		return nil, &StringMissingError{
			GoVersion: diag.GoVersion,
			GOARCH:    diag.GOARCH,
			Warnings:  append([]string(nil), diag.Warnings...),
		}
	}

	union := UnionReachable(matched)

	var systemUnion map[snapshotgraph.ObjectID]struct{}
	if !opts.IncludeSystemGoroutines && len(systemGoroutines) > 0 {
		systemUnion = make(map[snapshotgraph.ObjectID]struct{})
		for _, gr := range systemGoroutines {
			for id := range gr.Reachable {
				systemUnion[id] = struct{}{}
			}
		}
	}

	globalCount, globalBytes := IntersectCountBytes(g, union, analysis.Globals.Reachable)
	systemCount, systemBytes := IntersectCountBytes(g, union, systemUnion)

	attribution := AttributionHeapNative
	if diag.StringMissingCount > 0 || diag.FailedGoroutines > 0 {
		attribution = AttributionHeapNativeIncomplete
	}

	resp := &Response{
		Labels:               copyLabels(req.Labels),
		MatchedGoroutines:    len(matched),
		ReachableObjects:     len(union),
		ReachableBytes:       ObjectSetBytes(g, union),
		GlobalOverlapObjects: globalCount,
		GlobalOverlapBytes:   globalBytes,
		SystemOverlapObjects: systemCount,
		SystemOverlapBytes:   systemBytes,
		Attribution:          attribution,
		GoVersion:            diag.GoVersion,
		GOARCH:               diag.GOARCH,
		Warnings:             append([]string(nil), diag.Warnings...),
	}
	return resp, nil
}

// DiagnosticsFromHeapLabels converts a heaplabels.Result into Diagnostics.
// It is the bridge between the heap-native label decoder and the compute
// layer.
func DiagnosticsFromHeapLabels(snap *heapsnapshot.HeapSnapshot, res heaplabels.Result) Diagnostics {
	d := Diagnostics{}
	if snap != nil {
		d.GoVersion = snap.Params.BuildVersion
		d.GOARCH = snap.Params.GOARCH
	}
	d.StringMissingCount = res.Stats.StringsMissing
	d.FailedGoroutines = res.Stats.GoroutinesFailed
	if res.Stats.GoroutinesTotal > 0 && res.Stats.GoroutinesUnsupported == res.Stats.GoroutinesTotal {
		d.UnsupportedRuntime = true
	}
	if d.StringMissingCount > 0 {
		d.Warnings = append(d.Warnings,
			"heap-native label recovery found label structures but could not read all key/value strings")
	}
	if d.FailedGoroutines > 0 && d.StringMissingCount < d.FailedGoroutines {
		d.Warnings = append(d.Warnings,
			fmt.Sprintf("heap-native label recovery failed for %d goroutine(s)", d.FailedGoroutines))
	}
	d.Warnings = append(d.Warnings, res.Warnings...)
	return d
}

func copyLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
