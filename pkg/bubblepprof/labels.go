package bubblepprof

import (
	"context"
	"runtime/pprof"
	"strings"
)

// LabelSet pairs the pprof label set with a plain map so callers can
// inspect the labels without depending on the runtime/pprof internals.
//
// LabelSet is constructed with Labels and is safe to share across
// goroutines.
type LabelSet struct {
	pprofLabels pprof.LabelSet
	m           map[string]string
}

// Labels constructs a LabelSet from alternating key/value strings.
//
//	ls := bubblepprof.Labels("bubble", "alpha", "job", "42")
//
// Labels panics on an odd number of arguments. This matches
// runtime/pprof.Labels which also panics in that case.
func Labels(kv ...string) LabelSet {
	copied := cloneLabelStrings(kv)
	m := make(map[string]string, len(copied)/2)
	for i := 0; i+1 < len(copied); i += 2 {
		m[copied[i]] = copied[i+1]
	}
	return LabelSet{
		pprofLabels: pprof.Labels(copied...),
		m:           m,
	}
}

// Map returns a copy of the label set as a plain map.
func (l LabelSet) Map() map[string]string {
	out := make(map[string]string, len(l.m))
	for k, v := range l.m {
		out[k] = v
	}
	return out
}

// Len reports the number of distinct keys in the set.
func (l LabelSet) Len() int { return len(l.m) }

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

// Do runs f within a goroutine labeled with the supplied LabelSet. It
// behaves like runtime/pprof.Do.
//
// Nested Do calls restore the outer labels on exit, matching pprof.Do
// semantics.
func Do(ctx context.Context, labels LabelSet, f func(context.Context)) {
	pprof.Do(ctx, labels.pprofLabels, f)
}

// SetGoroutineLabels mirrors runtime/pprof.SetGoroutineLabels: it
// attaches the labels carried by ctx to the calling goroutine for the
// rest of its lifetime.
func SetGoroutineLabels(ctx context.Context) {
	pprof.SetGoroutineLabels(ctx)
}

// Go starts a new goroutine running f with ctx. The child goroutine has
// runtime/pprof labels from ctx applied via SetGoroutineLabels.
//
// Plain `go func(){...}` inherits pprof labels (because labels live in
// context) but does not explicitly call SetGoroutineLabels; Go is the
// supported way to ensure the child goroutine's pprof labels are
// attached before f starts.
func Go(ctx context.Context, f func(context.Context)) {
	go func() {
		pprof.SetGoroutineLabels(ctx)
		f(ctx)
	}()
}
