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
	// address-space reader the handler opens on Linux, macOS,
	// FreeBSD, and Windows so the heap-label decoder can recover
	// ordinary runtime/pprof string literals that live outside heap
	// object contents. When true, label decoding sees heap object
	// contents only, and ordinary pprof.Labels("job","42") may fail
	// with string_missing. Default false (reader enabled).
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

// MemUsageHandler returns an http.Handler that serves POST /debug/memusage
// with the default configuration: GC runs before the heap dump and
// system/background goroutines are excluded from label matching.
//
// On each request the handler stops all goroutines, captures a full heap
// dump, parses it with object contents retained, recovers pprof labels
// directly from heap-dump runtime state, builds a process-wide reachability
// graph, and returns the heap memory reachable from goroutines whose
// recovered labels contain every requested key/value pair.
//
// Security: this endpoint is equivalent in sensitivity to /debug/pprof.
// A single call can expose pprof label values (which may include tenant
// IDs, job identifiers, or other runtime metadata) and heap size
// breakdowns. Protect it with the same authentication and network
// controls you apply to /debug/pprof. It should never be exposed to
// untrusted callers.
//
// Performance: each request triggers a stop-the-world heap dump.
// Latency for the caller is proportional to live heap size. The handler
// enforces single in-flight execution — concurrent callers receive 429.
//
// Label compatibility: profiled code should use the standard
// runtime/pprof API directly. No bubblepprof wrapper is required:
//
//	pprof.Do(ctx, pprof.Labels("job", "42"), func(ctx context.Context) {
//	    runJob(ctx)
//	})
//
// Known limitations: heap-native label recovery is verified for
// go1.24.*–go1.26.* on Linux, macOS, Windows, and FreeBSD for
// little-endian 64-bit (amd64, arm64) and 32-bit (arm, 386) families;
// go1.27-devel (tip) is experimental. On unsupported runtime versions
// the endpoint returns HTTP 422 with code "unsupported_runtime". When
// pprof label strings reside outside heap object contents (common for
// string literals), the in-process reader is consulted on Linux, macOS,
// FreeBSD, and Windows; on other platforms or when disabled, the
// endpoint may return 422 with code "string_missing". Other structural
// label recovery failures return 422 with code "label_recovery_failed".
func MemUsageHandler() http.Handler {
	return MemUsageHandlerWithOptions(MemUsageOptions{})
}

// MemUsageHandlerWithOptions returns an http.Handler with the supplied
// MemUsageOptions. The zero value of MemUsageOptions mirrors MemUsageHandler().
// See MemUsageHandler for security and performance notes.
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

// RegisterMemUsage mounts MemUsageHandler at /debug/memusage on mux.
// It is intentionally separate from any default handler registration:
// callers must opt in explicitly because the endpoint is expensive,
// stop-the-world, and should be protected from untrusted callers.
// See MemUsageHandler for the full security and performance contract.
func RegisterMemUsage(mux *http.ServeMux) {
	mux.Handle(MemUsagePath, MemUsageHandler())
}

// RegisterMemUsageWithOptions mounts MemUsageHandlerWithOptions at
// /debug/memusage on mux. Use this when the zero-value MemUsageOptions
// defaults are not sufficient.
func RegisterMemUsageWithOptions(mux *http.ServeMux, opts MemUsageOptions) {
	mux.Handle(MemUsagePath, MemUsageHandlerWithOptions(opts))
}
