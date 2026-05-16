package memusage

// Attribution strings describe how labels were recovered. The /debug/memusage
// endpoint intentionally does not return labels_json/profile/mixed
// values in Phase 1 — the main endpoint only uses heap-native recovery.
const (
	AttributionHeapNative           = "heap_native"
	AttributionHeapNativeIncomplete = "heap_native_incomplete"
	AttributionUnsupportedRuntime   = "unsupported_runtime"
)

// Response is the success body for /debug/memusage.
type Response struct {
	Labels map[string]string `json:"labels"`

	MatchedGoroutines int `json:"matched_goroutines"`

	ReachableObjects int    `json:"reachable_objects"`
	ReachableBytes   uint64 `json:"reachable_bytes"`

	GlobalOverlapObjects int    `json:"global_overlap_objects,omitempty"`
	GlobalOverlapBytes   uint64 `json:"global_overlap_bytes,omitempty"`

	SystemOverlapObjects int    `json:"system_overlap_objects,omitempty"`
	SystemOverlapBytes   uint64 `json:"system_overlap_bytes,omitempty"`

	Attribution string   `json:"attribution"`
	GoVersion   string   `json:"go_version,omitempty"`
	GOARCH      string   `json:"goarch,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// ErrorResponse is the failure body for /debug/memusage. HTTP status is
// chosen by the handler, not this body.
type ErrorResponse struct {
	Error       string   `json:"error"`
	Code        string   `json:"code"`
	Attribution string   `json:"attribution,omitempty"`
	GoVersion   string   `json:"go_version,omitempty"`
	GOARCH      string   `json:"goarch,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}
