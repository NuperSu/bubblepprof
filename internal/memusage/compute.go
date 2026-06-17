package memusage

import (
	"fmt"

	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
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

// reachableFromGoroutines walks the graph once from the union of the
// supplied goroutines' Roots and returns the unioned reachable object
// set. This is the single-BFS replacement for "BFS each goroutine then
// union the results": same answer, paid for once.
func reachableFromGoroutines(g *snapshotgraph.Graph, goroutines []*snapshotgraph.GoroutineReachability) map[snapshotgraph.ObjectID]struct{} {
	if g == nil || len(goroutines) == 0 {
		return map[snapshotgraph.ObjectID]struct{}{}
	}
	rootCount := 0
	for _, gr := range goroutines {
		rootCount += len(gr.Roots)
	}
	if rootCount == 0 {
		return map[snapshotgraph.ObjectID]struct{}{}
	}
	roots := make([]snapshotgraph.RootRef, 0, rootCount)
	for _, gr := range goroutines {
		roots = append(roots, gr.Roots...)
	}
	return snapshotgraph.ReachableFrom(g, roots)
}

// ObjectSetBytes sums the shallow size of the objects in set, skipping
// IDs that are out of range (defensive; a normal Build never produces
// such IDs).
func ObjectSetBytes(g *snapshotgraph.Graph, set map[snapshotgraph.ObjectID]struct{}) uint64 {
	if g == nil {
		return 0
	}
	n := uint64(len(g.Objects))
	var bytes uint64
	for id := range set {
		if uint64(id) < n {
			bytes += g.Objects[id].Size
		}
	}
	return bytes
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
	n := uint64(len(g.Objects))
	var (
		count int
		bytes uint64
	)
	for id := range a {
		if _, ok := b[id]; !ok {
			continue
		}
		count++
		if uint64(id) < n {
			bytes += g.Objects[id].Size
		}
	}
	return count, bytes
}

// Diagnostics summarizes label-recovery state needed by ComputeFromAnalysis
// to decide whether to return a success response or an error.
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

	// StringMissingGIDs and FailedGIDs identify which goroutines failed
	// label decoding (FailedGIDs is a superset that includes the
	// string-missing goroutines). ComputeFromAnalysis uses them to ignore
	// failures on goroutines that are excluded from matching anyway
	// (system/background goroutines under the default options): an
	// excluded goroutine cannot change the match set, so its decode
	// failure must not fail the request. When both GID sets are nil but
	// the counts are non-zero (hand-constructed Diagnostics), every
	// counted failure is conservatively treated as match-eligible.
	StringMissingGIDs map[uint64]struct{}
	FailedGIDs        map[uint64]struct{}

	Warnings []string
}

// eligibleFailures reports how many label-decode failures affect
// match-eligible goroutines. known is the set of goroutine IDs present in
// the analysis; eligible is the subset allowed to participate in label
// matching. Failed GIDs that are known but not eligible are ignored;
// failed GIDs absent from known are treated as eligible (fail-explicit:
// a GID mismatch between label diagnostics and the graph means the match
// set cannot be trusted).
func (d Diagnostics) eligibleFailures(known, eligible map[uint64]struct{}) (stringMissing, failed int) {
	if d.FailedGIDs == nil && d.StringMissingGIDs == nil {
		// No per-GID detail: conservatively treat every counted failure
		// as match-eligible (preserves the strict pre-GID behavior).
		return d.StringMissingCount, d.FailedGoroutines
	}
	countsAsEligible := func(gid uint64) bool {
		if _, ok := eligible[gid]; ok {
			return true
		}
		_, isKnown := known[gid]
		return !isKnown
	}
	for gid := range d.FailedGIDs {
		if !countsAsEligible(gid) {
			continue
		}
		failed++
		if _, ok := d.StringMissingGIDs[gid]; ok {
			stringMissing++
		}
	}
	// Defensive: count string-missing GIDs that were not folded into
	// FailedGIDs (DiagnosticsFromHeapLabels always folds them in).
	for gid := range d.StringMissingGIDs {
		if _, ok := d.FailedGIDs[gid]; ok {
			continue
		}
		if !countsAsEligible(gid) {
			continue
		}
		failed++
		stringMissing++
	}
	return stringMissing, failed
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

// StringMissingError is returned by ComputeFromAnalysis when label decode
// failed because string bytes (key or value) were unavailable in the heap
// dump. The handler turns this into HTTP 422 with code "string_missing".
type StringMissingError struct {
	GoVersion string
	GOARCH    string
	Warnings  []string
}

func (e *StringMissingError) Error() string {
	return "pprof labels were found but some label string bytes were unavailable"
}

// LabelRecoveryFailedError is returned by ComputeFromAnalysis when heap-native
// label decode failed for reasons other than missing string bytes (e.g.
// g_object_missing, labels_object_missing, malformed map). The handler turns
// this into HTTP 422 with code "label_recovery_failed".
type LabelRecoveryFailedError struct {
	GoVersion        string
	GOARCH           string
	FailedGoroutines int
	Warnings         []string
}

func (e *LabelRecoveryFailedError) Error() string {
	return fmt.Sprintf("heap-native pprof label recovery failed for %d goroutine(s)", e.FailedGoroutines)
}

// CaptureFailedError is returned when the heap dump could not be written.
// The handler maps it to HTTP 500 with code "capture_failed".
type CaptureFailedError struct{ Cause error }

func (e *CaptureFailedError) Error() string { return "capture heap dump: " + e.Cause.Error() }
func (e *CaptureFailedError) Unwrap() error { return e.Cause }

// ParseFailedError is returned when the heap dump could not be parsed.
// The handler maps it to HTTP 500 with code "parse_failed".
type ParseFailedError struct{ Cause error }

func (e *ParseFailedError) Error() string { return "parse heap dump: " + e.Cause.Error() }
func (e *ParseFailedError) Unwrap() error { return e.Cause }

// ComputeFromAnalysis is the pure core of /debug/memusage: it takes a
// structural object graph (built by snapshotgraph.Build, without the
// optional ComputeReachability pass), a precomputed labelsByGID map,
// and label diagnostics, and returns the response payload.
//
// Reachability is traversed on demand: one BFS over the union of
// matched-goroutine roots, plus at most one BFS each for the global and
// system-goroutine overlap denominators (skipped entirely when nothing
// matches). This is the key reason Build no longer eagerly walks every
// goroutine: for a selector matching S of N goroutines we now pay
// O(reach(S)) instead of O(reach(N)).
//
// A label-decode failure makes the match set non-authoritative only when
// the failed goroutine could have participated in matching: an undecodable
// eligible goroutine might also carry the requested labels, so a partial
// or zero match count is not returned as 200. Failures on goroutines that
// are excluded from matching (system/background under the default options)
// cannot change the match set and are ignored. Two distinct 422 codes:
//
//   - eligible string-missing failures → StringMissingError (string bytes
//     unavailable)
//   - other eligible failures → LabelRecoveryFailedError
//
// Validation runs first so direct callers (e.g. unit tests, future CLI
// adapters) cannot bypass the same checks the HTTP handler applies.
// Errors are reported via concrete types the handler can translate into
// HTTP status codes:
//
//	*ValidationError           -> 400
//	*UnsupportedRuntimeError   -> 422
//	*StringMissingError        -> 422
//	*LabelRecoveryFailedError  -> 422
//	other                      -> 500
func ComputeFromAnalysis(
	req Request,
	analysis *snapshotgraph.Analysis,
	labelsByGID map[uint64]map[string]string,
	diag Diagnostics,
	opts Options,
) (*Response, error) {
	if verr := ValidateRequest(&req, opts); verr != nil {
		return nil, verr
	}
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

	matched := make([]*snapshotgraph.GoroutineReachability, 0)
	var systemGoroutines []*snapshotgraph.GoroutineReachability
	knownGIDs := make(map[uint64]struct{}, len(analysis.Goroutines))
	eligibleGIDs := make(map[uint64]struct{}, len(analysis.Goroutines))
	for i := range analysis.Goroutines {
		gr := &analysis.Goroutines[i]
		knownGIDs[gr.GoroutineID] = struct{}{}
		isSystem := gr.IsSystem || gr.IsBackground
		if isSystem {
			systemGoroutines = append(systemGoroutines, gr)
			if !opts.IncludeSystemGoroutines {
				continue
			}
		}
		eligibleGIDs[gr.GoroutineID] = struct{}{}
		have := labelsByGID[gr.GoroutineID]
		if !LabelsMatch(have, req.Labels) {
			continue
		}
		matched = append(matched, gr)
	}

	// A label-decode failure on a match-eligible goroutine makes the match
	// set non-authoritative: that goroutine might also carry the requested
	// labels. Failures on excluded system/background goroutines are ignored
	// — they cannot change the match set, and one permanently undecodable
	// runtime goroutine must not brick the endpoint. string_missing
	// (unavailable string bytes) takes priority over other decode failures
	// because it is the more actionable diagnosis.
	if err := eligibleFailureError(diag, knownGIDs, eligibleGIDs); err != nil {
		return nil, err
	}

	union := reachableFromGoroutines(g, matched)

	// Skip the overlap traversals when nothing matched. They would
	// intersect with an empty set and return zero anyway, and globals
	// alone can be large.
	var systemReach, globalReach map[snapshotgraph.ObjectID]struct{}
	if len(union) > 0 {
		globalReach = snapshotgraph.ReachableFrom(g, analysis.Globals.Roots)
		if !opts.IncludeSystemGoroutines && len(systemGoroutines) > 0 {
			systemReach = reachableFromGoroutines(g, systemGoroutines)
		}
	}

	globalCount, globalBytes := IntersectCountBytes(g, union, globalReach)
	systemCount, systemBytes := IntersectCountBytes(g, union, systemReach)

	resp := &Response{
		Labels:               copyLabels(req.Labels),
		MatchedGoroutines:    len(matched),
		ReachableObjects:     len(union),
		ReachableBytes:       ObjectSetBytes(g, union),
		GlobalOverlapObjects: globalCount,
		GlobalOverlapBytes:   globalBytes,
		SystemOverlapObjects: systemCount,
		SystemOverlapBytes:   systemBytes,
	}
	return resp, nil
}

func eligibleFailureError(diag Diagnostics, knownGIDs, eligibleGIDs map[uint64]struct{}) error {
	stringMissing, failed := diag.eligibleFailures(knownGIDs, eligibleGIDs)
	if stringMissing > 0 {
		return &StringMissingError{
			GoVersion: diag.GoVersion,
			GOARCH:    diag.GOARCH,
			Warnings:  append([]string{}, diag.Warnings...),
		}
	}
	if failed > 0 {
		return &LabelRecoveryFailedError{
			GoVersion:        diag.GoVersion,
			GOARCH:           diag.GOARCH,
			FailedGoroutines: failed,
			Warnings:         append([]string{}, diag.Warnings...),
		}
	}
	return nil
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
	for _, gr := range res.Goroutines {
		switch gr.Status {
		case heaplabels.StatusDecoded, heaplabels.StatusNoLabels, heaplabels.StatusUnsupportedRuntime:
			continue
		}
		if d.FailedGIDs == nil {
			d.FailedGIDs = make(map[uint64]struct{})
		}
		d.FailedGIDs[gr.GID] = struct{}{}
		if gr.Status == heaplabels.StatusStringMissing {
			if d.StringMissingGIDs == nil {
				d.StringMissingGIDs = make(map[uint64]struct{})
			}
			d.StringMissingGIDs[gr.GID] = struct{}{}
		}
	}
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
