// Package labelresolve attributes pprof labels to heap goroutines.
//
// The label source priority is:
//
//  1. heap-native labels — runtime.g.labels decoded from the same heap.dump
//     used to build the object graph. Exact and stop-the-world coherent
//     for supported Go runtime layouts.
//  2. manifest labels — labels.json emitted by the bubblepprof wrapper /
//     Registry. Exact, but requires application instrumentation.
//  3. profile labels — goroutine.pprof. Best-effort only: the profile is
//     captured at a different runtime moment, so goroutines may not
//     align. Disabled by default; opt in via Options.AllowProfileFallback
//     or Source=SourceModeProfile.
package labelresolve

import (
	"fmt"
	"strconv"
	"strings"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/runtimelayout"
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

// SourceMode picks the high-level label source policy. Use SourceModeAuto
// (the zero value) unless the caller knows it wants to restrict resolution
// to a single source.
type SourceMode string

const (
	// SourceModeAuto uses heap-native first, falls back to labels.json
	// for goroutines not covered by heap-native, and only consults the
	// goroutine profile when Options.AllowProfileFallback is true.
	SourceModeAuto SourceMode = ""
	// SourceModeHeap uses only heap-native recovery. If the runtime
	// layout is unsupported, no labels are assigned (and an explicit
	// error is returned when RequireHeapLabels is set).
	SourceModeHeap SourceMode = "heap"
	// SourceModeManifest uses only labels.json.
	SourceModeManifest SourceMode = "manifest"
	// SourceModeProfile uses only the goroutine pprof profile as a
	// best-effort source. Reports built from this source must be flagged
	// as best-effort.
	SourceModeProfile SourceMode = "profile"
)

// Options tunes ResolveLabels.
type Options struct {
	// Source picks the label-source policy. See SourceMode.
	Source SourceMode

	// AllowProfileFallback is consulted only in SourceModeAuto. When
	// true, the resolver may attach labels from the goroutine pprof
	// profile to goroutines still unmatched after heap and manifest
	// resolution. Reports built with profile fallback are best-effort.
	AllowProfileFallback bool

	// RequireHeapLabels causes ResolveLabels to record a strong warning
	// (and report it via Diagnostics.UnsupportedHeapLayout) if heap-native
	// recovery is unavailable. Useful for SourceModeHeap callers that
	// want to surface an actionable error.
	RequireHeapLabels bool

	// DisableHeap turns off runtime.g.labels heap-dump-native recovery
	// even when the requested source would otherwise enable it. Provided
	// as an escape hatch for diagnostics; prefer Source.
	DisableHeap bool
	// DisableManifest turns off labels.json. Prefer Source.
	DisableManifest bool
	// DisableProfile turns off pprof correlation entirely. Prefer Source
	// and AllowProfileFallback.
	DisableProfile bool

	// HeapLabels configures heap-dump-native decoding limits
	// (MaxLabels, MaxStringLen). The runtime layout itself is resolved
	// through internal/runtimelayout; callers cannot override the
	// runtime.g.labels offset here, by design.
	HeapLabels heaplabels.Options
}

// effectivePolicy resolves the per-source booleans the rest of the
// function uses. The returned values describe whether each source is
// allowed to contribute labels in the current call.
type effectivePolicy struct {
	useHeap    bool
	useManifest bool
	useProfile bool
	// profileBestEffort marks that any profile match must be reported
	// as best-effort attribution.
	profileBestEffort bool
}

func (o Options) policy() effectivePolicy {
	p := effectivePolicy{}
	switch o.Source {
	case SourceModeHeap:
		p.useHeap = !o.DisableHeap
	case SourceModeManifest:
		p.useManifest = !o.DisableManifest
	case SourceModeProfile:
		p.useProfile = !o.DisableProfile
		p.profileBestEffort = true
	default: // SourceModeAuto
		p.useHeap = !o.DisableHeap
		p.useManifest = !o.DisableManifest
		if o.AllowProfileFallback && !o.DisableProfile {
			p.useProfile = true
			p.profileBestEffort = true
		}
	}
	return p
}

// AttributionMode is a short label describing the source mix that
// produced the resolution. It is suitable for printing in summaries.
type AttributionMode string

const (
	AttributionNone             AttributionMode = "no_labels"
	AttributionHeapNative       AttributionMode = "heap_native_exact"
	AttributionManifest         AttributionMode = "manifest_exact"
	AttributionMixedExact       AttributionMode = "mixed_exact_heap_and_manifest"
	AttributionBestEffortProfile AttributionMode = "best_effort_profile_fallback"
)

func (m AttributionMode) Description() string {
	switch m {
	case AttributionHeapNative:
		return "heap-native exact (runtime.g.labels from heap.dump)"
	case AttributionManifest:
		return "labels.json exact"
	case AttributionMixedExact:
		return "mixed exact: heap-native + labels.json"
	case AttributionBestEffortProfile:
		return "best effort: goroutine.pprof fallback used"
	default:
		return "no labels available"
	}
}

// Diagnostics summarizes how a Resolution was produced. Designed for
// printing alongside bubble reports so users can tell exact attribution
// from best-effort attribution at a glance.
type Diagnostics struct {
	Mode SourceMode

	HeapNativeMatches int
	ManifestMatches   int
	ProfileMatches    int

	ProfileFallbackAllowed bool
	ProfileFallbackUsed    bool

	UnsupportedHeapLayout bool
	HeapLayoutReason      string
	MissingStringBytes    int

	AmbiguousProfileMatches int

	UnmatchedHeapGoroutines int
	UnmatchedProfileSamples int

	Attribution AttributionMode
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

	Diagnostics Diagnostics

	Warnings []string
}

// ResolveLabels attaches labels to heap goroutines using heap-native
// recovery as the primary exact source, labels.json as an exact fallback,
// and the goroutine pprof profile as an opt-in best-effort fallback.
//
// The function never panics on nil inputs; missing inputs are reported
// through Warnings and the diagnostic counters.
func ResolveLabels(
	snap *heapsnapshot.HeapSnapshot,
	manifest *bubblelabels.Manifest,
	prof *goroutineprofile.Profile,
	opts Options,
) Resolution {
	policy := opts.policy()
	res := Resolution{
		LabelsByGID:   make(map[uint64]map[string]string),
		SourcesByGID:  make(map[uint64]Source),
		AmbiguousGIDs: make(map[uint64]struct{}),
	}
	res.Diagnostics.Mode = opts.Source
	res.Diagnostics.ProfileFallbackAllowed = policy.useProfile

	if snap == nil {
		res.Warnings = append(res.Warnings, "labelresolve: nil heap snapshot")
		res.Diagnostics.Attribution = AttributionNone
		return res
	}
	res.HeapGoroutines = len(snap.Goroutines)
	heapIDs := make(map[uint64]struct{}, len(snap.Goroutines))
	for _, g := range snap.Goroutines {
		heapIDs[g.ID] = struct{}{}
	}

	if policy.useHeap {
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
			res.Diagnostics.MissingStringBytes = heapRes.Stats.StringsMissing
			if heapRes.Stats.GoroutinesFailed > 0 || heapRes.Stats.StringsMissing > 0 {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("heap label recovery decoded %d goroutines, failed %d, string bytes missing %d",
						heapRes.Stats.GoroutinesDecoded,
						heapRes.Stats.GoroutinesFailed,
						heapRes.Stats.StringsMissing))
			}
		} else {
			res.Diagnostics.UnsupportedHeapLayout = true
			res.Diagnostics.HeapLayoutReason = reason
			if reason != "" {
				res.Warnings = append(res.Warnings, reason)
			}
			if opts.RequireHeapLabels {
				res.Warnings = append(res.Warnings,
					"heap-native labels required but unavailable; no labels were assigned from heap.dump")
			}
		}
	}

	if policy.useManifest && manifest != nil {
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
	} else if manifest != nil {
		res.ManifestSize = len(manifest.Goroutines)
	}

	if prof != nil {
		res.ProfileSamples = len(prof.Goroutines)
	}

	if !policy.useProfile || prof == nil {
		if opts.Source == SourceModeAuto && !opts.AllowProfileFallback && prof != nil && hasLabeledSamples(prof) {
			res.Warnings = append(res.Warnings,
				"goroutine.pprof contains labeled samples but profile fallback is disabled (pass --allow-profile-fallback or --labels-source=profile to opt in; it is best-effort because the profile is not stop-the-world coherent with heap.dump)")
		}
		for id := range heapIDs {
			if _, ok := res.LabelsByGID[id]; !ok {
				res.UnmatchedHeap++
			}
		}
		finalizeDiagnostics(&res, policy)
		return res
	}

	// Profile fallback path.
	res.Diagnostics.ProfileFallbackUsed = true
	matchedSamples := make(map[int]bool, len(prof.Goroutines))
	for idx, sample := range prof.Goroutines {
		if id, ok := sampleGoroutineID(sample); ok {
			if _, inHeap := heapIDs[id]; !inHeap {
				continue
			}
			if _, alreadyResolved := res.LabelsByGID[id]; alreadyResolved {
				matchedSamples[idx] = true
				continue
			}
			res.LabelsByGID[id] = copyLabels(sample.Labels)
			res.SourcesByGID[id] = SourceProfileID
			res.MatchedFromProfile++
			matchedSamples[idx] = true
		}
	}

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
		if !sameLabelSets(pb.labelSets) {
			for _, gid := range hb.ids {
				res.AmbiguousGIDs[gid] = struct{}{}
			}
			res.AmbiguousMatches += len(hb.ids)
			continue
		}
		labels := pb.labelSets[0]
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
	if res.MatchedFromProfile > 0 {
		res.Warnings = append(res.Warnings,
			"goroutine.pprof fallback used: profile is best-effort and may not correspond to heap-dump goroutine state")
	}

	finalizeDiagnostics(&res, policy)
	return res
}

func finalizeDiagnostics(res *Resolution, policy effectivePolicy) {
	d := &res.Diagnostics
	d.HeapNativeMatches = res.MatchedFromHeap
	d.ManifestMatches = res.MatchedFromManifest
	d.ProfileMatches = res.MatchedFromProfile
	d.AmbiguousProfileMatches = res.AmbiguousMatches
	d.UnmatchedHeapGoroutines = res.UnmatchedHeap
	d.UnmatchedProfileSamples = res.UnmatchedProfile

	switch {
	case res.MatchedFromProfile > 0:
		d.Attribution = AttributionBestEffortProfile
	case res.MatchedFromHeap > 0 && res.MatchedFromManifest > 0:
		d.Attribution = AttributionMixedExact
	case res.MatchedFromHeap > 0:
		d.Attribution = AttributionHeapNative
	case res.MatchedFromManifest > 0:
		d.Attribution = AttributionManifest
	default:
		d.Attribution = AttributionNone
	}
	_ = policy
}

func hasLabeledSamples(prof *goroutineprofile.Profile) bool {
	if prof == nil {
		return false
	}
	for _, s := range prof.Goroutines {
		if len(s.Labels) > 0 || len(s.Numeric) > 0 {
			return true
		}
	}
	return false
}

func resolveHeapLabels(snap *heapsnapshot.HeapSnapshot, opts heaplabels.Options) (heaplabels.Result, bool, string) {
	if snap == nil {
		return heaplabels.Result{}, false, "heap label recovery skipped: nil heap snapshot"
	}
	if !hasObjectContents(snap) {
		return heaplabels.Result{}, false, "heap label recovery skipped: heap object contents were not retained"
	}
	input := heaplabels.LookupInputFromSnapshot(snap)
	layout, ok := runtimelayout.Lookup(input)
	if !ok {
		return heaplabels.Result{}, false, fmt.Sprintf(
			"heap label recovery unsupported: %s", runtimelayout.UnsupportedMessage(input),
		)
	}
	return heaplabels.DecodeAll(snap, layout, opts), true, ""
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
//
// These keys are correlation-only; callers must drop them before
// exposing labels as bubble identity.
func sampleGoroutineID(s goroutineprofile.GoroutineSample) (uint64, bool) {
	for _, k := range correlationLabelKeys {
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

// correlationLabelKeys are profile-only label keys that identify the
// goroutine itself. They must not be exposed as bubble identity.
var correlationLabelKeys = [...]string{"goid", "goroutine_id", "goroutine"}

// IsCorrelationLabelKey reports whether a label key is a profile
// correlation marker (e.g. "goid") rather than a real bubble label.
func IsCorrelationLabelKey(key string) bool {
	for _, k := range correlationLabelKeys {
		if key == k {
			return true
		}
	}
	return false
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
