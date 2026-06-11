package memusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// ComputeFunc is the request-side surface used by the handler. Production
// wires it to (*Computer).Compute; tests can pass a fake.
type ComputeFunc func(ctx context.Context, req Request) (*Response, error)

// HandlerOptions configures Handler.
type HandlerOptions struct {
	Opts                Options
	MaxRequestBodyBytes int64

	// Semaphore is the single-flight gate (capacity-1 channel). When nil
	// the handler creates a private one. Supplying a shared channel lets
	// multiple heap-dumping endpoints (e.g. /debug/memusage and
	// /debug/memusage/bundle) serialize against each other, since each
	// triggers a stop-the-world WriteHeapDump.
	Semaphore chan struct{}
}

// Handler returns an http.Handler that serves POST /debug/memusage by
// delegating heavy lifting to compute. The handler enforces a single
// in-flight request at a time and returns 429 if a second arrives.
//
// HTTP semantics:
//
//	POST -> JSON success or JSON error
//	other methods -> 405 with Allow: POST
//
// On structural errors (bad JSON, validation failures) the body is a
// JSON ErrorResponse with HTTP 400. Unsupported-runtime, string-missing,
// and structural label-recovery diagnostics map to HTTP 422.
func Handler(compute ComputeFunc, hopts HandlerOptions) http.Handler {
	if compute == nil {
		panic("memusage.Handler: nil compute func")
	}
	maxBody := hopts.MaxRequestBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxRequestBodyBytes
	}
	sem := hopts.Semaphore
	if sem == nil {
		sem = make(chan struct{}, 1)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, compute, hopts.Opts, maxBody, sem)
	})
}

func serve(
	w http.ResponseWriter,
	r *http.Request,
	compute ComputeFunc,
	opts Options,
	maxBody int64,
	sem chan struct{},
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, &ErrorResponse{
			Error: "method not allowed; use POST",
			Code:  "invalid_method",
		})
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxBody)
	defer body.Close()

	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	var req Request
	if err := dec.Decode(&req); err != nil {
		// MaxBytesReader returns its own error; normalize to a 400.
		writeError(w, http.StatusBadRequest, &ErrorResponse{
			Error: "invalid JSON request body",
			Code:  "invalid_request",
		})
		return
	}
	// Reject trailing data after a complete JSON value: callers MUST
	// send exactly one JSON document. (json.Decoder.More is documented
	// for arrays/objects, not top-level streams — a second Decode that
	// fails to return io.EOF is the reliable check.)
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, &ErrorResponse{
			Error: "request body must contain exactly one JSON object",
			Code:  "invalid_request",
		})
		return
	}

	if verr := ValidateRequest(&req, opts); verr != nil {
		writeError(w, http.StatusBadRequest, &ErrorResponse{
			Error: verr.Msg,
			Code:  verr.Code,
		})
		return
	}

	// Single-flight gate: WriteHeapDump is stop-the-world; concurrent
	// callers get 429 instead of a queue.
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		writeError(w, http.StatusTooManyRequests, &ErrorResponse{
			Error: "another memusage request is already running",
			Code:  "busy",
		})
		return
	}

	resp, err := compute(r.Context(), req)
	if err != nil {
		writeComputeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeComputeError(w http.ResponseWriter, err error) {
	status, body := ErrorResponseFor(err)
	writeError(w, status, body)
}

// ErrorResponseFor maps a compute/analyse error to the HTTP status code
// and JSON error body served by the /debug/memusage endpoint. The CLI
// uses the same mapping so error codes (unsupported_runtime,
// string_missing, label_recovery_failed, capture_failed, parse_failed,
// validation codes) are identical in both modes.
func ErrorResponseFor(err error) (int, *ErrorResponse) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return http.StatusBadRequest, &ErrorResponse{
			Error: validation.Msg,
			Code:  validation.Code,
		}
	}
	var unsupported *UnsupportedRuntimeError
	if errors.As(err, &unsupported) {
		return http.StatusUnprocessableEntity, &ErrorResponse{
			Error:     unsupported.Error(),
			Code:      "unsupported_runtime",
			GoVersion: unsupported.GoVersion,
			GOARCH:    unsupported.GOARCH,
		}
	}
	var stringMissing *StringMissingError
	if errors.As(err, &stringMissing) {
		return http.StatusUnprocessableEntity, &ErrorResponse{
			Error:     stringMissing.Error(),
			Code:      "string_missing",
			GoVersion: stringMissing.GoVersion,
			GOARCH:    stringMissing.GOARCH,
			Warnings:  stringMissing.Warnings,
		}
	}
	var labelRecoveryFailed *LabelRecoveryFailedError
	if errors.As(err, &labelRecoveryFailed) {
		return http.StatusUnprocessableEntity, &ErrorResponse{
			Error:     labelRecoveryFailed.Error(),
			Code:      "label_recovery_failed",
			GoVersion: labelRecoveryFailed.GoVersion,
			GOARCH:    labelRecoveryFailed.GOARCH,
			Warnings:  labelRecoveryFailed.Warnings,
		}
	}
	var captureFailed *CaptureFailedError
	if errors.As(err, &captureFailed) {
		return http.StatusInternalServerError, &ErrorResponse{
			Error: captureFailed.Error(),
			Code:  "capture_failed",
		}
	}
	var parseFailed *ParseFailedError
	if errors.As(err, &parseFailed) {
		return http.StatusInternalServerError, &ErrorResponse{
			Error: parseFailed.Error(),
			Code:  "parse_failed",
		}
	}
	return http.StatusInternalServerError, &ErrorResponse{
		Error: err.Error(),
		Code:  "internal_error",
	}
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(body) // best-effort: status header already sent
}

func writeError(w http.ResponseWriter, status int, body *ErrorResponse) {
	if body.Warnings == nil {
		body.Warnings = []string{}
	}
	writeJSON(w, status, body)
}
