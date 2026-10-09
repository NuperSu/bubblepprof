package memusage

import (
	"fmt"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

// AllocationEvidence is sampled origin information, separate from root paths.
// Interior samples (e.g. tiny allocations) retain their offset within the object.
type AllocationEvidence struct {
	SampleAddress string                  `json:"sample_address"`
	BucketAddress string                  `json:"bucket_address"`
	Offset        uint64                  `json:"offset_in_object"`
	Profile       heapsnapshot.MemProfile `json:"profile"`
}

func (s *Snapshot) retainAllocations(snap *heapsnapshot.HeapSnapshot) {
	s.allocations = map[snapshotgraph.ObjectID][]AllocationEvidence{}
	buckets := map[uint64]heapsnapshot.MemProfile{}
	duplicates := map[uint64]bool{}
	for _, b := range snap.MemProfiles {
		if _, ok := buckets[b.Bucket]; ok {
			duplicates[b.Bucket] = true
		}
		buckets[b.Bucket] = b
	}
	unresolved := 0
	for _, sample := range snap.AllocationSamples {
		b, ok := buckets[sample.Bucket]
		id, found := s.analysis.Graph.FindObjectContaining(sample.Address)
		if !ok || !found || duplicates[sample.Bucket] {
			unresolved++
			continue
		}
		s.allocations[id] = append(s.allocations[id], AllocationEvidence{SampleAddress: fmt.Sprintf("0x%x", sample.Address), BucketAddress: fmt.Sprintf("0x%x", sample.Bucket), Offset: sample.Address - s.analysis.Graph.Objects[id].Addr, Profile: b})
	}
	if unresolved > 0 {
		s.metadata.Warnings = append(s.metadata.Warnings, fmt.Sprintf("%d allocation sample(s) could not be associated unambiguously with an object and profile bucket", unresolved))
	}
}
func cloneAllocations(in []AllocationEvidence) []AllocationEvidence {
	if len(in) == 0 {
		return nil
	}
	out := append([]AllocationEvidence(nil), in...)
	for i := range out {
		out[i].Profile.Frames = append([]heapsnapshot.AllocationFrame(nil), out[i].Profile.Frames...)
	}
	return out
}
