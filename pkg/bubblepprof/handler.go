package bubblepprof

import (
	"fmt"
	"log"
	"net/http"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/snapshot"
)

const snapshotPath = "/debug/bubblepprof/snapshot"

type Options struct {
	GCBeforeHeapDump bool

	// IncludeGoroutineStacks toggles whether the snapshot tar embeds a
	// debug=2 goroutine stack dump alongside the protobuf goroutine
	// profile. Default true.
	IncludeGoroutineStacks bool

	// IncludeLabelsManifest toggles whether the snapshot embeds a
	// labels.json built from the bubblepprof Registry. Default true;
	// has no effect if the registry is empty.
	IncludeLabelsManifest bool
}

func Handler() http.Handler {
	return HandlerWithOptions(Options{
		GCBeforeHeapDump:       true,
		IncludeGoroutineStacks: true,
		IncludeLabelsManifest:  true,
	})
}

func HandlerWithOptions(opts Options) http.Handler {
	captureOpts := capture.CaptureOptions{GCBeforeHeapDump: opts.GCBeforeHeapDump}
	if opts.IncludeLabelsManifest {
		captureOpts.LabelManifestProvider = capture.RegistryLabelManifestProvider{
			Registry: defaultRegistry,
			Source:   registrySourceID,
		}
	}
	if opts.IncludeGoroutineStacks {
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
