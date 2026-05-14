package bubblepprof

import (
	"fmt"
	"log"
	"net/http"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/snapshot"
)

const snapshotPath = "/debug/bubblepprof/snapshot"

// Options configures the snapshot HTTP handler.
//
// The zero value is a sensible default: heap.dump + metadata.json are
// always emitted; labels.json is emitted whenever the bubblepprof
// Registry has entries; goroutine.pprof and goroutine.stacks diagnostics
// are emitted. Set the Disable* fields to opt out.
//
// goroutine.pprof and goroutine.stacks are diagnostics — they are
// captured separately from the stop-the-world heap dump and therefore
// only support best-effort label attribution. Exact bubble reports come
// from heap-native recovery (and labels.json fallback).
type Options struct {
	// GCBeforeHeapDump runs runtime.GC() before WriteHeapDump. Default
	// false (zero value); Handler() enables it.
	GCBeforeHeapDump bool

	// DisableLabelManifest skips the labels.json fallback even when the
	// Registry has entries. Default false.
	DisableLabelManifest bool

	// DisableDiagnostics skips the goroutine.stacks diagnostic entry.
	// Default false.
	DisableDiagnostics bool
}

// Handler returns an http.Handler with a sensible default configuration:
// runtime.GC() before the heap dump, labels.json emitted when the
// Registry has entries, and diagnostics (goroutine.stacks) enabled.
func Handler() http.Handler {
	return HandlerWithOptions(Options{GCBeforeHeapDump: true})
}

// HandlerWithOptions returns an http.Handler with the supplied Options.
// Options{} is a sensible default (labels.json + diagnostics on,
// GCBeforeHeapDump off).
func HandlerWithOptions(opts Options) http.Handler {
	captureOpts := capture.CaptureOptions{GCBeforeHeapDump: opts.GCBeforeHeapDump}
	if !opts.DisableLabelManifest {
		captureOpts.LabelManifestProvider = capture.RegistryLabelManifestProvider{
			Registry: defaultRegistry,
			Source:   registrySourceID,
		}
	}
	if !opts.DisableDiagnostics {
		captureOpts.GoroutineStacksWriter = capture.RuntimeGoroutineStacksWriter{}
	}
	return handler(captureOpts)
}

func Register(mux *http.ServeMux) {
	mux.Handle(snapshotPath, Handler())
}

func handler(captureOpts capture.CaptureOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		opts := captureOpts
		if r.URL.Query().Get("gc") == "0" {
			opts.GCBeforeHeapDump = false
		}

		// Capture first so heap-dump or goroutine-profile errors can be
		// reported as 5xx before any response body is written. The actual
		// tar bundle then streams straight into the response writer —
		// avoiding a second double-buffer of the whole snapshot to disk
		// (the heap dump itself is already on disk inside the capture).
		captured, err := capture.Capture(r.Context(), opts)
		if err != nil {
			http.Error(w, fmt.Sprintf("capture snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		defer captured.Cleanup()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="bubblepprof-snapshot.tar"`)
		w.WriteHeader(http.StatusOK)
		if err := snapshot.WriteSnapshotBundle(w, captured.BundleSource()); err != nil {
			log.Printf("bubblepprof: snapshot bundle write failed mid-stream: %v", err)
			return
		}
	})
}
