package bubblepprof

import (
	"context"
	"runtime/pprof"
	"strings"
	"time"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/goid"
)

// LabelSet pairs bubblepprof's persisted ordered label set with the
// equivalent runtime/pprof label set so Do and Go can transparently set
// real pprof labels in addition to recording the mapping.
//
// LabelSet is constructed with Labels and is safe to share across
// goroutines.
type LabelSet struct {
	pprofLabels pprof.LabelSet
	labels      bubblelabels.LabelSet
}

// Labels constructs a LabelSet from alternating key/value strings.
//
//	ls := bubblepprof.Labels("bubble", "alpha", "job", "42")
//
// Labels panics on an odd number of arguments. This matches
// runtime/pprof.Labels which also panics in that case.
func Labels(kv ...string) LabelSet {
	copied := cloneLabelStrings(kv)
	internal, err := bubblelabels.MakeLabelSet(copied...)
	if err != nil {
		panic(err)
	}
	return LabelSet{
		pprofLabels: pprof.Labels(copied...),
		labels:      internal,
	}
}

// Map returns a copy of the label set as a plain map.
func (l LabelSet) Map() map[string]string { return l.labels.Map() }

// Len reports the number of distinct keys in the set.
func (l LabelSet) Len() int { return l.labels.Len() }

func cloneLabelStrings(kv []string) []string {
	out := make([]string, len(kv))
	for i, s := range kv {
		// Heap-native label recovery decodes runtime/pprof's labelMap
		// out of heap.dump object bytes. String literals can point at
		// read-only program data that WriteHeapDump does not preserve as
		// object contents, so the wrapper feeds pprof heap-owned copies.
		out[i] = strings.Clone(s)
	}
	return out
}

// DefaultRegistry is the registry that the package-level wrappers write
// into. Tests can swap it via UseRegistry.
var (
	defaultRegistry  = bubblelabels.NewRegistry()
	registrySourceID = "bubblepprof.Do"
)

// Registry returns the registry that the package-level helpers write into.
// It is exposed primarily so the capture path and tests can read it.
func Registry() *bubblelabels.Registry { return defaultRegistry }

// UseRegistry replaces the active registry. Returns the previous value so
// tests can restore it.
func UseRegistry(r *bubblelabels.Registry) *bubblelabels.Registry {
	prev := defaultRegistry
	if r != nil {
		defaultRegistry = r
	}
	return prev
}

// SnapshotLabels returns a Manifest containing one entry per currently
// labeled goroutine. The Manifest is a copy and safe to mutate.
func SnapshotLabels() bubblelabels.Manifest {
	return defaultRegistry.Snapshot(time.Now(), registrySourceID)
}

// Do runs f within a goroutine labeled with the supplied LabelSet. It
// behaves like runtime/pprof.Do and additionally records the labels in
// bubblepprof's per-goroutine registry as a fallback label source.
//
// Nested Do calls restore the outer labels on exit (the registry uses a
// per-goroutine stack), matching pprof.Do semantics.
func Do(ctx context.Context, labels LabelSet, f func(context.Context)) {
	pprof.Do(ctx, labels.pprofLabels, func(ctx context.Context) {
		id, ok := goid.CurrentGoroutineID()
		var pop func()
		if ok {
			pop = defaultRegistry.Push(id, labels.labels.Map())
		}
		defer func() {
			if pop != nil {
				pop()
			}
		}()
		f(ctx)
	})
}

// SetGoroutineLabels mirrors runtime/pprof.SetGoroutineLabels: it
// attaches the labels carried by ctx to the calling goroutine for the
// rest of its lifetime. In addition, it records the mapping in the
// bubblepprof registry under the current goroutine ID.
//
// Use SetGoroutineLabels when adopting an already-running goroutine that
// was not started via Do or Go.
func SetGoroutineLabels(ctx context.Context) {
	pprof.SetGoroutineLabels(ctx)
	id, ok := goid.CurrentGoroutineID()
	if !ok {
		return
	}
	defaultRegistry.Set(id, labelsFromContext(ctx))
}

// RegisterCurrentGoroutine records the bubblepprof labels carried by ctx
// against the calling goroutine, without touching runtime/pprof state.
// This is rarely needed directly; prefer Do, Go, or SetGoroutineLabels.
func RegisterCurrentGoroutine(ctx context.Context) {
	id, ok := goid.CurrentGoroutineID()
	if !ok {
		return
	}
	defaultRegistry.Set(id, labelsFromContext(ctx))
}

// Go starts a new goroutine running f with ctx. The child goroutine has
// runtime/pprof labels from ctx applied via SetGoroutineLabels, and its
// goroutine ID is recorded in the bubblepprof registry until f returns.
//
// Plain `go func(){...}` inherits pprof labels (because labels live in
// context) but bubblepprof has no way to learn the new goroutine's ID
// without explicit registration; Go is the supported way to do that.
func Go(ctx context.Context, f func(context.Context)) {
	go func() {
		pprof.SetGoroutineLabels(ctx)
		id, ok := goid.CurrentGoroutineID()
		if ok {
			defaultRegistry.Set(id, labelsFromContext(ctx))
			defer defaultRegistry.Clear(id)
		}
		f(ctx)
	}()
}

// labelsFromContext extracts the pprof labels carried by ctx into a plain
// map. It uses runtime/pprof.ForLabels which is the only public way to
// enumerate the labels attached to a context.
func labelsFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	var out map[string]string
	pprof.ForLabels(ctx, func(key, value string) bool {
		if out == nil {
			out = make(map[string]string)
		}
		out[key] = value
		return true
	})
	return out
}
