package bundle

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// HandlerOptions configures Handler.
type HandlerOptions struct {
	Capture CaptureOptions

	// Semaphore is the single-flight gate (capacity-1 channel). When nil
	// the handler creates a private one. Supplying the same channel used
	// by the /debug/memusage handler serializes the two endpoints, since
	// each triggers a stop-the-world WriteHeapDump.
	Semaphore chan struct{}
}

// Handler returns an http.Handler that serves GET /debug/memusage/bundle
// by streaming a capture bundle of the current process.
//
// HTTP semantics:
//
//	GET            -> application/x-tar bundle stream
//	GET?gc=0|1     -> overrides Capture.GCBeforeHeapDump for this request
//	other methods  -> 405 with Allow: GET
//
// Errors before the first body byte produce a JSON ErrorResponse
// (capture_failed, 500). Once streaming has started the only honest
// failure mode is aborting the connection, so the client sees a
// truncated tar instead of a falsely complete one.
func Handler(hopts HandlerOptions) http.Handler {
	sem := hopts.Semaphore
	if sem == nil {
		sem = make(chan struct{}, 1)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveBundle(w, r, hopts.Capture, sem)
	})
}

func serveBundle(w http.ResponseWriter, r *http.Request, copts CaptureOptions, sem chan struct{}) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, &memusage.ErrorResponse{
			Error: "method not allowed; use GET",
			Code:  "invalid_method",
		})
		return
	}

	switch gc := r.URL.Query().Get("gc"); gc {
	case "":
	case "0", "false":
		copts.GCBeforeHeapDump = false
	case "1", "true":
		copts.GCBeforeHeapDump = true
	default:
		writeJSONError(w, http.StatusBadRequest, &memusage.ErrorResponse{
			Error: fmt.Sprintf("invalid gc parameter %q; use 0, 1, true, or false", gc),
			Code:  "invalid_request",
		})
		return
	}

	// Single-flight gate: WriteHeapDump is stop-the-world; concurrent
	// callers get 429 instead of a queue.
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		writeJSONError(w, http.StatusTooManyRequests, &memusage.ErrorResponse{
			Error: "another memusage request is already running",
			Code:  "busy",
		})
		return
	}

	bw := &bundleResponseWriter{w: w}
	if err := CaptureSelf(r.Context(), bw, copts); err != nil {
		if !bw.wroteBody {
			status, body := memusage.ErrorResponseFor(&memusage.CaptureFailedError{Cause: err})
			writeJSONError(w, status, body)
			return
		}
		// Headers already sent; abort the connection so the client sees
		// a truncated transfer rather than a complete-looking tar.
		panic(http.ErrAbortHandler)
	}
}

// bundleResponseWriter defers the tar response headers until the first
// body byte so pre-stream capture failures can still produce a JSON
// error response.
type bundleResponseWriter struct {
	w         http.ResponseWriter
	wroteBody bool
}

func (bw *bundleResponseWriter) Write(p []byte) (int, error) {
	if !bw.wroteBody {
		bw.wroteBody = true
		bw.w.Header().Set("Content-Type", "application/x-tar")
		bw.w.Header().Set("Content-Disposition", "attachment; filename="+bundleFilename())
		bw.w.WriteHeader(http.StatusOK)
	}
	return bw.w.Write(p)
}

func bundleFilename() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("bubblepprof-%s-%d.tar", host, time.Now().Unix())
}

func writeJSONError(w http.ResponseWriter, status int, body *memusage.ErrorResponse) {
	if body.Warnings == nil {
		body.Warnings = []string{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
