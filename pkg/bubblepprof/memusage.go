package bubblepprof

import (
	"net/http"

	"bubblepprof/internal/memusage"
)

// MemUsagePath is the canonical path under which MemUsageHandler is
// expected to be mounted.
const MemUsagePath = "/debug/memusage"

// MemUsageOptions controls the behavior of MemUsageHandler.
//
// MemUsageHandler is an expensive, stop-the-world debugging endpoint.
// Protect it the same way you protect /debug/pprof in production: it
// triggers a full heap dump on each call and can expose sensitive
// memory information.
//
// All bool fields use negative defaults (Disable*, Include*) so the
// zero-value MemUsageOptions{} matches MemUsageHandler()'s defaults:
// GC before the heap dump, exclude system goroutines.
type MemUsageOptions struct {
	// DisableGCBeforeHeapDump turns off the runtime.GC() that
	// MemUsageHandler runs immediately before debug.WriteHeapDump. The
	// default (false) keeps the GC enabled.
	DisableGCBeforeHeapDump bool

	// IncludeSystemGoroutines lets system/background goroutines
	// participate in label matching. Default false.
	IncludeSystemGoroutines bool

	// DisableProcessMemoryReader turns off the in-process
	// address-space reader the handler opens (when running on
	// Linux) so the heap-label decoder can recover ordinary
	// runtime/pprof string literals that live outside heap object
	// contents. When true, label decoding sees heap object contents
	// only, and ordinary pprof.Labels("job","42") may fail with
	// status_missing. Default false (reader enabled).
	DisableProcessMemoryReader bool

	// MaxRequestBodyBytes caps the request body. Zero falls back to the
	// internal default (1 MiB).
	MaxRequestBodyBytes int64

	// Resource limits applied during validation. Zero falls back to the
	// internal defaults.
	MaxLabels          int
	MaxLabelKeyBytes   int
	MaxLabelValueBytes int
}

// MemUsageHandler returns an http.Handler that serves
// POST /debug/memusage with the default configuration: GC runs before
// the heap dump and system/background goroutines are excluded from
// matching.
//
// The handler captures a heap dump on each request, parses it with
// object contents retained, builds a process-wide reachability graph,
// recovers pprof labels directly from heap-dump runtime state, and
// returns the heap memory reachable from goroutines whose recovered
// labels contain every requested key/value pair.
//
// The handler is intended for diagnostic use, similar to
// net/http/pprof's Index. It should not be exposed to untrusted
// callers.
func MemUsageHandler() http.Handler {
	return MemUsageHandlerWithOptions(MemUsageOptions{})
}

// MemUsageHandlerWithOptions returns an http.Handler with the supplied
// MemUsageOptions. The zero value mirrors MemUsageHandler().
func MemUsageHandlerWithOptions(opts MemUsageOptions) http.Handler {
	internal := opts.toInternal()
	computer := memusage.NewComputer(internal)
	return memusage.Handler(computer.Compute, memusage.HandlerOptions{
		Opts:                internal,
		MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
	})
}

// toInternal maps the public MemUsageOptions onto the internal
// memusage.Options. It exists so the bool-flip and zero-value defaults
// can be unit-tested without spinning up an HTTP server.
func (o MemUsageOptions) toInternal() memusage.Options {
	return memusage.Options{
		GCBeforeHeapDump:           !o.DisableGCBeforeHeapDump,
		IncludeSystemGoroutines:    o.IncludeSystemGoroutines,
		DisableProcessMemoryReader: o.DisableProcessMemoryReader,
		MaxLabels:                  o.MaxLabels,
		MaxLabelKeyBytes:           o.MaxLabelKeyBytes,
		MaxLabelValueBytes:         o.MaxLabelValueBytes,
	}
}

// RegisterMemUsage mounts MemUsageHandler at /debug/memusage on mux. It
// is intentionally separate from Register: callers should opt in
// explicitly because the endpoint is expensive and stop-the-world.
func RegisterMemUsage(mux *http.ServeMux) {
	mux.Handle(MemUsagePath, MemUsageHandler())
}
