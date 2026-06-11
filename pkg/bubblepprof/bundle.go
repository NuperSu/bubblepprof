package bubblepprof

import (
	"net/http"
	"runtime/debug"
	"sync"

	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// MemUsageBundlePath is the canonical path under which BundleHandler is
// expected to be mounted.
const MemUsageBundlePath = "/debug/memusage/bundle"

// modulePath is this library's module path as it appears in the
// embedding binary's build info.
const modulePath = "github.com/NuperSu/bubblepprof"

// producer returns the string recorded in bundle metadata so an
// analyser can tell which library version produced an artifact. The
// version comes from the embedding binary's build info; when it is not
// available (e.g. built from a workspace or this module's own tests)
// the version part is omitted.
var producer = sync.OnceValue(func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		mods := append([]*debug.Module{&bi.Main}, bi.Deps...)
		for _, m := range mods {
			if m.Replace != nil {
				m = m.Replace
			}
			if m.Path == modulePath && m.Version != "" && m.Version != "(devel)" {
				return "bubblepprof/" + m.Version
			}
		}
	}
	return "bubblepprof"
})

// BundleOptions controls the behavior of BundleHandler.
//
// All bool fields use negative defaults (Disable*) so the zero value
// matches BundleHandler()'s defaults: GC before the heap dump, rodata
// snapshot enabled.
type BundleOptions struct {
	// DisableGCBeforeHeapDump turns off the runtime.GC() that runs
	// immediately before debug.WriteHeapDump. The default (false) keeps
	// the GC enabled. A request may override per call with ?gc=0|1.
	DisableGCBeforeHeapDump bool

	// DisableRodataCapture skips the read-only memory segment snapshot.
	// Without it, an external analyser cannot recover ordinary
	// runtime/pprof string literals (e.g. pprof.Labels("job", "42"))
	// and such labels surface as string_missing during analysis.
	DisableRodataCapture bool

	// MaxRodataBytes caps the total read-only segment bytes embedded in
	// a bundle. Zero falls back to the internal default (256 MiB).
	// Segments that do not fit are dropped and the bundle's rodata
	// status is recorded as "truncated".
	MaxRodataBytes int64
}

// BundleHandler returns an http.Handler that serves
// GET /debug/memusage/bundle with default options. The response is a
// tar stream ("bundle") containing a heap dump of the current process,
// a snapshot of its read-only memory segments, and metadata — the input
// for the external analyser (the bubblepprof CLI), which runs the same
// analysis as the in-process /debug/memusage endpoint without spending
// the target's CPU and memory on it.
//
// Security: a bundle contains the full heap dump and read-only program
// memory of the process — strictly more sensitive than the
// /debug/memusage JSON response and equivalent to /debug/pprof heap and
// goroutine dumps. Protect this endpoint with the same authentication
// and network controls you apply to /debug/pprof. It must never be
// exposed to untrusted callers.
//
// Performance: each request triggers a stop-the-world heap dump, but no
// parsing or graph analysis happens in-process; the target only streams
// files. The handler enforces single in-flight execution — concurrent
// callers receive 429.
func BundleHandler() http.Handler {
	return BundleHandlerWithOptions(BundleOptions{})
}

// BundleHandlerWithOptions returns an http.Handler with the supplied
// BundleOptions. The zero value mirrors BundleHandler(). See
// BundleHandler for security and performance notes.
func BundleHandlerWithOptions(opts BundleOptions) http.Handler {
	return bundleHandler(opts, nil)
}

func bundleHandler(opts BundleOptions, sem chan struct{}) http.Handler {
	return bundle.Handler(bundle.HandlerOptions{
		Capture: bundle.CaptureOptions{
			GCBeforeHeapDump: !opts.DisableGCBeforeHeapDump,
			DisableRodata:    opts.DisableRodataCapture,
			MaxRodataBytes:   opts.MaxRodataBytes,
			Producer:         producer(),
		},
		Semaphore: sem,
	})
}

// RegisterBundle mounts BundleHandler at /debug/memusage/bundle on mux.
// Callers must opt in explicitly; see BundleHandler for the security
// contract. To serve both the in-process endpoint and the bundle
// endpoint, prefer Register, which makes the two share one single-flight
// gate.
func RegisterBundle(mux *http.ServeMux) {
	mux.Handle(MemUsageBundlePath, BundleHandler())
}

// RegisterBundleWithOptions mounts BundleHandlerWithOptions at
// /debug/memusage/bundle on mux.
func RegisterBundleWithOptions(mux *http.ServeMux, opts BundleOptions) {
	mux.Handle(MemUsageBundlePath, BundleHandlerWithOptions(opts))
}

// Register mounts both debugging endpoints on mux with default options:
//
//	POST /debug/memusage        — in-process analysis (MemUsageHandler)
//	GET  /debug/memusage/bundle — capture artifact for the external
//	                              analyser CLI (BundleHandler)
//
// The two handlers share one single-flight gate: each triggers a
// stop-the-world heap dump, so while either request is running the
// other endpoint responds 429 busy.
func Register(mux *http.ServeMux) {
	RegisterWithOptions(mux, MemUsageOptions{}, BundleOptions{})
}

// RegisterWithOptions is Register with explicit options for each
// endpoint.
func RegisterWithOptions(mux *http.ServeMux, mo MemUsageOptions, bo BundleOptions) {
	sem := make(chan struct{}, 1)

	internal := mo.toInternal()
	computer := memusage.NewComputer(internal)
	mux.Handle(MemUsagePath, memusage.Handler(computer.Compute, memusage.HandlerOptions{
		Opts:                internal,
		MaxRequestBodyBytes: mo.MaxRequestBodyBytes,
		Semaphore:           sem,
	}))
	mux.Handle(MemUsageBundlePath, bundleHandler(bo, sem))
}
