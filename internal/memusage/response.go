package memusage

// Response is the success body for /debug/memusage.
type Response struct {
	Labels map[string]string `json:"labels"`

	MatchedGoroutines int `json:"matched_goroutines"`

	ReachableObjects int    `json:"reachable_objects"`
	ReachableBytes   uint64 `json:"reachable_bytes"`

	GlobalOverlapObjects int    `json:"global_overlap_objects"`
	GlobalOverlapBytes   uint64 `json:"global_overlap_bytes"`

	SystemOverlapObjects int    `json:"system_overlap_objects"`
	SystemOverlapBytes   uint64 `json:"system_overlap_bytes"`
}

// ErrorResponse is the failure body for /debug/memusage. HTTP status is
// chosen by the handler, not this body.
type ErrorResponse struct {
	Error     string   `json:"error"`
	Code      string   `json:"code"`
	GoVersion string   `json:"go_version,omitempty"`
	GOARCH    string   `json:"goarch,omitempty"`
	Warnings  []string `json:"warnings"`
}
