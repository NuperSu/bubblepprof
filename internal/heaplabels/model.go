// Package heaplabels prototypes recovery of runtime/pprof goroutine labels
// directly from the heap dump's runtime.g objects.
//
// This package intentionally depends on runtime-internal layouts. It is an
// experiment: callers must provide or discover the runtime.g.labels offset,
// and every failure is reported explicitly rather than silently falling back.
package heaplabels

type Result struct {
	LabelsByGID map[uint64]map[string]string
	Goroutines  []GoroutineResult
	Stats       Stats
	Warnings    []string
}

type GoroutineResult struct {
	GID       uint64
	GAddr     uint64
	LabelsPtr uint64

	Labels map[string]string

	Status DecodeStatus
	Error  string
}

type DecodeStatus string

const (
	StatusDecoded             DecodeStatus = "decoded"
	StatusNoLabels            DecodeStatus = "no_labels"
	StatusUnsupportedRuntime  DecodeStatus = "unsupported_runtime"
	StatusGObjectMissing      DecodeStatus = "g_object_missing"
	StatusLabelsObjectMissing DecodeStatus = "labels_object_missing"
	StatusLabelArrayMissing   DecodeStatus = "label_array_missing"
	StatusStringMissing       DecodeStatus = "string_missing"
	StatusMalformed           DecodeStatus = "malformed"
)

type Stats struct {
	GoroutinesTotal       int
	GoroutinesDecoded     int
	GoroutinesNoLabels    int
	GoroutinesUnsupported int
	GoroutinesFailed      int

	LabelsTotal    int
	StringsMissing int
}

type OffsetCandidate struct {
	Offset       uint64
	Matches      int
	GoroutineIDs []uint64
}
