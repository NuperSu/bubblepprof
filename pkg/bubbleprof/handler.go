package bubbleprof

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"delve_first_project/internal/capture"
)

const snapshotPath = "/debug/bubbleprof/snapshot"

type Options struct {
	GCBeforeHeapDump bool
}

func Handler() http.Handler {
	return HandlerWithOptions(Options{GCBeforeHeapDump: true})
}

func HandlerWithOptions(opts Options) http.Handler {
	return handler(capture.CaptureOptions{GCBeforeHeapDump: opts.GCBeforeHeapDump})
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

		tmp, err := os.CreateTemp("", "bubbleprof-snapshot-*.tar")
		if err != nil {
			http.Error(w, fmt.Sprintf("create snapshot temp file: %v", err), http.StatusInternalServerError)
			return
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		defer tmp.Close()

		if err := capture.WriteSnapshot(r.Context(), tmp, opts); err != nil {
			http.Error(w, fmt.Sprintf("capture snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			http.Error(w, fmt.Sprintf("rewind snapshot: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="bubbleprof-snapshot.tar"`)
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, tmp); err != nil {
			return
		}
	})
}
