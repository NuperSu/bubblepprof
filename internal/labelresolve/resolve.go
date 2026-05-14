// Package labelresolve combines an exact bubblelabels.Manifest and a
// best-effort goroutineprofile.Profile into a final goroutine ID -> pprof
// labels mapping. The resolver never invents labels: a heap goroutine
// that cannot be matched conservatively stays unlabeled, and the
// diagnostics record the reason.
package labelresolve

import (
	"fmt"
	"strconv"
	"strings"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
)

// Source describes where one goroutine's labels came from.
type Source int

const (
	// SourceNone means the goroutine has no labels.
	SourceNone Source = iota
	// SourceManifest means labels came from labels.json.
	SourceManifest
	// SourceHeap means labels came from runtime.g.labels decoded out of
	// the same heap.dump used for reachability.
	SourceHeap
	// SourceProfileID means labels came from a pprof sample carrying a
	// "goid"/"goroutine" numeric or string label.
	SourceProfileID
	// SourceProfileStack means labels came from a conservative
	// stack-signature correlation against the pprof profile.
	SourceProfileStack
)

func (s Source) String() string {
	switch s {
	case SourceHeap:
		return "heap.dump"
	case SourceManifest:
		return "labels.json"
	case SourceProfileID:
		return "pprof.id"
	case SourceProfileStack:
		return "pprof.stack"
	default:
		return "none"
	}
}

// Resolution is the final per-goroutine label decision plus enough
// auxiliary information to build diagnostics and bubble reports.
type Resolution struct {
	LabelsByGID   map[uint64]map[string]string
	SourcesByGID  map[uint64]Source
	AmbiguousGIDs map[uint64]struct{}

	HeapGoroutines int
	ProfileSamples int
	ManifestSize   int

	MatchedFromManifest int
	MatchedFromHeap     int
	MatchedFromProfile  int
	UnmatchedHeap       int
	UnmatchedProfile    int
	AmbiguousMatches    int

	Warnings []string
}

// Options tunes ResolveLabels.
type Options struct {
	// DisableHeap turns off runtime.g.labels heap-dump-native recovery.
	DisableHeap bool
	// HeapOnly refuses all non-heap label sources.
	HeapOnly bool
	// DisableProfile turns off best-effort pprof correlation entirely.
	DisableProfile bool
	// ManifestOnly is a stronger flag: refuse all non-manifest sources.
	ManifestOnly bool
	// ProfileOnly refuses heap and manifest labels and uses only
	// best-effort pprof profile correlation.
	ProfileOnly bool
	// HeapLabels configures heap-dump-native decoding. If no explicit
	// GLabelsOffset is provided, ResolveLabels uses the verified layout
	// table in internal/heaplabels.
	HeapLabels heaplabels.Options
}

// ResolveLabels attaches labels to heap goroutines using the manifest as
// the primary source and the goroutine pprof profile as a conservative
// fallback. The function never panics on nil inputs; missing inputs are
// reported through Warnings and the diagnostic counters.
func ResolveLabels(
	snap *heapsnapshot.HeapSnapshot,
	manifest *bubblelabels.Manifest,
	prof *goroutineprofile.Profile,
	opts Options,
) Resolution {
	res := Resolution{
		LabelsByGID:   make(map[uint64]map[string]string),
		SourcesByGID:  make(map[uint64]Source),
		AmbiguousGIDs: make(map[uint64]struct{}),
	}
	if snap == nil {
		res.Warnings = append(res.Warnings, "labelresolve: nil heap snapshot")
		return res
	}
	res.HeapGoroutines = len(snap.Goroutines)
	heapIDs := make(map[uint64]struct{}, len(snap.Goroutines))
	for _, g := range snap.Goroutines {
		heapIDs[g.ID] = struct{}{}
	}

	if !opts.DisableHeap && !opts.ManifestOnly && !opts.ProfileOnly {
		heapRes, attempted, reason := resolveHeapLabels(snap, opts.HeapLabels)
		if attempted {
			for gid, labels := range heapRes.LabelsByGID {
				if _, ok := heapIDs[gid]; !ok {
					continue
				}
				res.LabelsByGID[gid] = copyLabels(labels)
				res.SourcesByGID[gid] = SourceHeap
				res.MatchedFromHeap++
			}
			if heapRes.Stats.GoroutinesFailed > 0 || heapRes.Stats.StringsMissing > 0 {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("heap label recovery decoded %d goroutines, failed %d, string bytes missing %d",
						heapRes.Stats.GoroutinesDecoded,
						heapRes.Stats.GoroutinesFailed,
						heapRes.Stats.StringsMissing))
			}
		} else if reason != "" {
			res.Warnings = append(res.Warnings, reason)
		}
	}

	if manifest != nil && !opts.HeapOnly && !opts.ProfileOnly {
		res.ManifestSize = len(manifest.Goroutines)
		for _, e := range manifest.Goroutines {
			if _, ok := heapIDs[e.ID]; !ok {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("manifest goroutine %d not present in heap dump", e.ID))
				continue
			}
			if source, dup := res.SourcesByGID[e.ID]; dup {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("manifest entry for goroutine %d ignored; labels already resolved from %s", e.ID, source))
				continue
			}
			if _, dup := res.LabelsByGID[e.ID]; dup {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("manifest contains duplicate entry for goroutine %d; keeping last", e.ID))
			} else {
				res.MatchedFromManifest++
			}
			res.LabelsByGID[e.ID] = copyLabels(e.Labels)
			res.SourcesByGID[e.ID] = SourceManifest
		}
	}

	if prof != nil {
		res.ProfileSamples = len(prof.Goroutines)
	}
	if prof == nil || opts.ManifestOnly || opts.DisableProfile || opts.HeapOnly {
		// All remaining heap goroutines stay unlabeled.
		for id := range heapIDs {
			if _, ok := res.LabelsByGID[id]; !ok {
				res.UnmatchedHeap++
			}
		}
		return res
	}

	// Step 1: try direct goroutine ID labels in the pprof profile. The
	// Go runtime does not currently emit one, but third-party
	// instrumentation may. This step costs almost nothing and is exact
	// when present.
	matchedSamples := make(map[int]bool, len(prof.Goroutines))
	for idx, sample := range prof.Goroutines {
		if id, ok := sampleGoroutineID(sample); ok {
			if _, inHeap := heapIDs[id]; !inHeap {
				continue
			}
			if _, alreadyManifest := res.LabelsByGID[id]; alreadyManifest {
				matchedSamples[idx] = true
				continue
			}
			res.LabelsByGID[id] = copyLabels(sample.Labels)
			res.SourcesByGID[id] = SourceProfileID
			res.MatchedFromProfile++
			matchedSamples[idx] = true
		}
	}

	// Step 2: bucket unlabeled heap goroutines by stack signature, and
	// labeled profile samples by stack signature, then match
	// conservatively.
	type heapBucket struct{ ids []uint64 }
	heapBySig := make(map[string]*heapBucket)
	for _, g := range snap.Goroutines {
		if _, ok := res.LabelsByGID[g.ID]; ok {
			continue
		}
		sig := HeapStackSignature(g)
		if sig == "" {
			continue
		}
		b := heapBySig[sig]
		if b == nil {
			b = &heapBucket{}
			heapBySig[sig] = b
		}
		b.ids = append(b.ids, g.ID)
	}

	type profileBucket struct {
		count          int64
		labelSets      []map[string]string
		sampleIdxs     []int
		matchedAlready bool
	}
	profBySig := make(map[string]*profileBucket)
	for idx, sample := range prof.Goroutines {
		if len(sample.Labels) == 0 {
			continue
		}
		if matchedSamples[idx] {
			continue
		}
		sig := ProfileStackSignature(sample)
		if sig == "" {
			continue
		}
		b := profBySig[sig]
		if b == nil {
			b = &profileBucket{}
			profBySig[sig] = b
		}
		b.count += sample.Count
		b.labelSets = append(b.labelSets, sample.Labels)
		b.sampleIdxs = append(b.sampleIdxs, idx)
	}

	for sig, hb := range heapBySig {
		pb, ok := profBySig[sig]
		if !ok {
			continue
		}
		// Combine label sets into a single canonical map if they all
		// agree. If they disagree we mark the bucket ambiguous.
		if !sameLabelSets(pb.labelSets) {
			for _, gid := range hb.ids {
				res.AmbiguousGIDs[gid] = struct{}{}
			}
			res.AmbiguousMatches += len(hb.ids)
			continue
		}
		labels := pb.labelSets[0]
		// Conservative rule: either one heap goroutine and one
		// profile sample with count==1, or N heap goroutines and a
		// profile total of N with one consistent label set.
		switch {
		case len(hb.ids) == 1 && pb.count == 1:
			res.LabelsByGID[hb.ids[0]] = copyLabels(labels)
			res.SourcesByGID[hb.ids[0]] = SourceProfileStack
			res.MatchedFromProfile++
			pb.matchedAlready = true
		case int64(len(hb.ids)) == pb.count:
			for _, gid := range hb.ids {
				res.LabelsByGID[gid] = copyLabels(labels)
				res.SourcesByGID[gid] = SourceProfileStack
				res.MatchedFromProfile++
			}
			pb.matchedAlready = true
		default:
			// Counts disagree: do not guess. Mark heap goroutines
			// ambiguous so diagnostics surface the case.
			for _, gid := range hb.ids {
				res.AmbiguousGIDs[gid] = struct{}{}
			}
			res.AmbiguousMatches += len(hb.ids)
		}
	}

	for id := range heapIDs {
		if _, ok := res.LabelsByGID[id]; !ok {
			res.UnmatchedHeap++
		}
	}
	for _, b := range profBySig {
		if !b.matchedAlready {
			res.UnmatchedProfile += len(b.sampleIdxs)
		}
	}

	return res
}

func resolveHeapLabels(snap *heapsnapshot.HeapSnapshot, opts heaplabels.Options) (heaplabels.Result, bool, string) {
	if snap == nil {
		return heaplabels.Result{}, false, "heap label recovery skipped: nil heap snapshot"
	}
	if !hasObjectContents(snap) {
		return heaplabels.Result{}, false, "heap label recovery skipped: heap object contents were not retained"
	}
	if !opts.HasGLabelsOffset {
		off, ok := heaplabels.LookupGLabelsOffset(snap)
		if !ok {
			return heaplabels.Result{}, false, fmt.Sprintf("heap label recovery unsupported for build=%q goarch=%q ptrSize=%d",
				snap.Params.BuildVersion, snap.Params.GOARCH, snap.Params.PtrSize)
		}
		opts.GLabelsOffset = off
		opts.HasGLabelsOffset = true
	}
	return heaplabels.DecodeAll(snap, opts), true, ""
}

func hasObjectContents(snap *heapsnapshot.HeapSnapshot) bool {
	for _, obj := range snap.Objects {
		if len(obj.Contents) > 0 {
			return true
		}
	}
	return false
}

// HeapStackSignature builds a normalized signature from a heap dump
// goroutine's frame list. Frame order matches the heap dump: top of
// stack first.
func HeapStackSignature(g heapsnapshot.Goroutine) string {
	names := make([]string, 0, len(g.Frames))
	for _, f := range g.Frames {
		n := normalizeFuncName(f.FuncName)
		if n == "" {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "|")
}

// ProfileStackSignature builds the equivalent signature from a profile
// sample. Profile frames are also callee-first within each location.
func ProfileStackSignature(s goroutineprofile.GoroutineSample) string {
	names := make([]string, 0, len(s.Frames))
	for _, f := range s.Frames {
		n := normalizeFuncName(f.Func)
		if n == "" {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "|")
}

func normalizeFuncName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || n == "runtime.goexit" {
		return ""
	}
	return n
}

// sampleGoroutineID returns the goroutine ID encoded in the sample's
// labels under a small set of well-known keys. It returns (0, false)
// when no such label is present.
func sampleGoroutineID(s goroutineprofile.GoroutineSample) (uint64, bool) {
	for _, k := range [...]string{"goid", "goroutine_id", "goroutine"} {
		if v, ok := s.Labels[k]; ok {
			if id, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil && id != 0 {
				return id, true
			}
		}
		if v, ok := s.Numeric[k]; ok && v > 0 {
			return uint64(v), true
		}
	}
	return 0, false
}

func sameLabelSets(sets []map[string]string) bool {
	if len(sets) <= 1 {
		return true
	}
	first := sets[0]
	for _, m := range sets[1:] {
		if len(m) != len(first) {
			return false
		}
		for k, v := range first {
			if m[k] != v {
				return false
			}
		}
	}
	return true
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
