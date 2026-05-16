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
type MemUsageOptions struct {
	// GCBeforeHeapDump runs runtime.GC() immediately before
	// debug.WriteHeapDump. Default true via MemUsageHandler.
	GCBeforeHeapDump bool

	// IncludeSystemGoroutines lets system/background goroutines
	// participate in label matching. Default false.
	IncludeSystemGoroutines bool

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
// POST /debug/memusage with a sensible default configuration:
// GCBeforeHeapDump=true and IncludeSystemGoroutines=false.
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
	return MemUsageHandlerWithOptions(MemUsageOptions{GCBeforeHeapDump: true})
}

// MemUsageHandlerWithOptions returns an http.Handler with the supplied
// MemUsageOptions.
func MemUsageHandlerWithOptions(opts MemUsageOptions) http.Handler {
	computer := memusage.NewComputer(memusage.Options{
		GCBeforeHeapDump:        opts.GCBeforeHeapDump,
		IncludeSystemGoroutines: opts.IncludeSystemGoroutines,
		MaxLabels:               opts.MaxLabels,
		MaxLabelKeyBytes:        opts.MaxLabelKeyBytes,
		MaxLabelValueBytes:      opts.MaxLabelValueBytes,
	})
	return memusage.Handler(computer.Compute, memusage.HandlerOptions{
		Opts: memusage.Options{
			GCBeforeHeapDump:        opts.GCBeforeHeapDump,
			IncludeSystemGoroutines: opts.IncludeSystemGoroutines,
			MaxLabels:               opts.MaxLabels,
			MaxLabelKeyBytes:        opts.MaxLabelKeyBytes,
			MaxLabelValueBytes:      opts.MaxLabelValueBytes,
		},
		MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
	})
}

// RegisterMemUsage mounts MemUsageHandler at /debug/memusage on mux. It
// is intentionally separate from Register: callers should opt in
// explicitly because the endpoint is expensive and stop-the-world.
func RegisterMemUsage(mux *http.ServeMux) {
	mux.Handle(MemUsagePath, MemUsageHandler())
}
