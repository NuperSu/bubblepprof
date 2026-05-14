// Package memusage implements the core computation behind the
// /debug/memusage HTTP endpoint: it pairs heap-dump-derived reachability
// with heap-native pprof label recovery so callers can ask "how much heap
// is reachable from goroutines carrying these standard runtime/pprof
// labels?".
//
// The package is split between pure computation (validation, label
// matching, object-set arithmetic, ComputeFromAnalysis) and a small
// production helper that captures a real heap dump, parses it, builds the
// graph, and recovers labels before delegating to ComputeFromAnalysis.
// Tests exercise the pure half with synthetic snapshotgraph.Analysis
// inputs so they do not need to write real heap dumps.
package memusage

import "fmt"

const (
	DefaultMaxLabels           = 32
	DefaultMaxLabelKeyBytes    = 1024
	DefaultMaxLabelValueBytes  = 4096
	DefaultMaxRequestBodyBytes = 1 << 20 // 1 MiB
)

// Request is the JSON body POSTed to /debug/memusage.
type Request struct {
	Labels map[string]string `json:"labels"`
}

// Options tune the behavior of Compute and ComputeFromAnalysis.
type Options struct {
	// GCBeforeHeapDump triggers runtime.GC() immediately before
	// debug.WriteHeapDump in the production capture path. The pure
	// compute path ignores it.
	GCBeforeHeapDump bool

	// IncludeSystemGoroutines lets system/background goroutines (g0, GC
	// workers, finalizer goroutine, etc.) participate in label matching
	// like ordinary user goroutines. Default false.
	IncludeSystemGoroutines bool

	// Resource limits. Zero values fall back to the Default* constants.
	MaxLabels          int
	MaxLabelKeyBytes   int
	MaxLabelValueBytes int
}

// ValidationError describes a structural problem with the request body
// that should map to a 400 response.
type ValidationError struct {
	Code string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Msg }

// NewValidationError is a small helper so callers can construct one in a
// single line.
func NewValidationError(code, msg string) *ValidationError {
	return &ValidationError{Code: code, Msg: msg}
}

func (o Options) effectiveMaxLabels() int {
	if o.MaxLabels <= 0 {
		return DefaultMaxLabels
	}
	return o.MaxLabels
}

func (o Options) effectiveMaxLabelKeyBytes() int {
	if o.MaxLabelKeyBytes <= 0 {
		return DefaultMaxLabelKeyBytes
	}
	return o.MaxLabelKeyBytes
}

func (o Options) effectiveMaxLabelValueBytes() int {
	if o.MaxLabelValueBytes <= 0 {
		return DefaultMaxLabelValueBytes
	}
	return o.MaxLabelValueBytes
}

// ValidateRequest enforces the request-body constraints documented for
// Phase 1: non-empty labels map, non-empty keys, and per-option limits on
// label count and key/value length.
func ValidateRequest(req *Request, opts Options) *ValidationError {
	if req == nil {
		return NewValidationError("invalid_request", "missing request body")
	}
	if len(req.Labels) == 0 {
		return NewValidationError("empty_labels", "labels must contain at least one key/value pair")
	}
	maxLabels := opts.effectiveMaxLabels()
	if len(req.Labels) > maxLabels {
		return NewValidationError("too_many_labels", fmt.Sprintf("labels map has %d entries, max is %d", len(req.Labels), maxLabels))
	}
	maxKey := opts.effectiveMaxLabelKeyBytes()
	maxVal := opts.effectiveMaxLabelValueBytes()
	for k, v := range req.Labels {
		if k == "" {
			return NewValidationError("empty_label_key", "label key must be non-empty")
		}
		if len(k) > maxKey {
			return NewValidationError("label_key_too_long", fmt.Sprintf("label key length %d exceeds max %d", len(k), maxKey))
		}
		if len(v) > maxVal {
			return NewValidationError("label_value_too_long", fmt.Sprintf("label value length %d exceeds max %d", len(v), maxVal))
		}
	}
	return nil
}
