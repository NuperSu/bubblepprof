package memusage

import (
	"context"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"testing"
)

func TestAllocationEvidence(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	addr := s.analysis.Graph.Objects[2].Addr
	snap := &heapsnapshot.HeapSnapshot{MemProfiles: []heapsnapshot.MemProfile{{Bucket: 7, Size: 2, Frames: []heapsnapshot.AllocationFrame{{Function: "main.allocate", File: "main.go", Line: 42}}, Allocs: 9, Frees: 3}}, AllocationSamples: []heapsnapshot.AllocationSample{{Address: addr + 1, Bucket: 7}, {Address: addr, Bucket: 99}, {Address: 0, Bucket: 7}}}
	s.retainAllocations(snap)
	out, err := s.Objects(context.Background(), selector("a"), ObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range out.Objects {
		if o.ID != 2 {
			if len(o.AllocationSamples) > 0 {
				t.Fatal("invented origin")
			}
			continue
		}
		found = true
		if len(o.AllocationSamples) != 1 || o.AllocationSamples[0].Offset != 1 || o.AllocationSamples[0].Profile.Frames[0].Function != "main.allocate" {
			t.Fatal(o)
		}
		o.AllocationSamples[0].Profile.Frames[0].Function = "mutated"
	}
	if !found || s.allocations[2][0].Profile.Frames[0].Function != "main.allocate" || len(s.Metadata().Warnings) < 1 {
		t.Fatalf("found=%v evidence=%+v metadata=%+v", found, s.allocations[2], s.Metadata())
	}
	snap.MemProfiles = append(snap.MemProfiles, snap.MemProfiles[0])
	s.retainAllocations(snap)
	if len(s.allocations) != 0 {
		t.Fatal("ambiguous bucket must not infer origin")
	}
}
