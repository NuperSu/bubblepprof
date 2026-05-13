package snapshot

import "time"

const (
	FormatV1 = "bubblepprof-snapshot-v1"

	HeapDumpFile         = "heap.dump"
	GoroutineProfileFile = "goroutine.pprof"
	MetadataFile         = "metadata.json"
	LabelsFile           = "labels.json"
	GoroutineStacksFile  = "goroutine.stacks"
)

type SnapshotMetadata struct {
	Format               string            `json:"format"`
	CreatedAt            time.Time         `json:"created_at"`
	GoVersion            string            `json:"go_version"`
	PID                  int               `json:"pid"`
	HeapDumpFile         string            `json:"heap_dump_file"`
	GoroutineProfileFile string            `json:"goroutine_profile_file"`
	LabelsFile           string            `json:"labels_file,omitempty"`
	GoroutineStacksFile  string            `json:"goroutine_stacks_file,omitempty"`
	GCBeforeHeapDump     bool              `json:"gc_before_heap_dump"`
	Extra                map[string]string `json:"extra,omitempty"`
}
