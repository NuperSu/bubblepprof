package memusage

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

func inspectionFixture(t *testing.T) *Snapshot {
	t.Helper()
	// A -> 0 -> 2 -> 3 -> 2; B -> 1 -> 2. Globals -> 2.
	// A -> 4 is exclusive, an unlabeled goroutine -> 5 overlaps A -> 5.
	snap := &heapsnapshot.HeapSnapshot{Objects: []heapsnapshot.Object{
		{Addr: 0x1000, Size: 16, PointerAddrs: []uint64{0x3004}, PointerOffsets: []uint64{8}},
		{Addr: 0x2000, Size: 8, PointerAddrs: []uint64{0x3000}, PointerOffsets: []uint64{0}},
		{Addr: 0x3000, Size: 64, PointerAddrs: []uint64{0x4000}, PointerOffsets: []uint64{16}},
		{Addr: 0x4000, Size: 4, PointerAddrs: []uint64{0x3000}, PointerOffsets: []uint64{0}},
		{Addr: 0x5000, Size: 1024},
		{Addr: 0x6000, Size: 4},
		{Addr: 0x7000, Size: 0},
	}, Goroutines: []heapsnapshot.Goroutine{
		{ID: 1, Frames: []heapsnapshot.StackFrame{{FuncName: "workerA", SP: 0x8000, CurrentPC: 0x9000, PointerAddrs: []uint64{0x1000, 0x5000, 0x6000, 0x7000}, PointerSlots: []uint64{0x8000, 0x8008, 0x8010, 0x8018}}}},
		{ID: 2, Frames: []heapsnapshot.StackFrame{{FuncName: "workerB", PointerAddrs: []uint64{0x2000}}}},
		{ID: 3, Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x6000}}}},
		{ID: 4, IsSystem: true, Frames: []heapsnapshot.StackFrame{{PointerAddrs: []uint64{0x4000}}}},
	}, Globals: []heapsnapshot.Root{{Kind: "bss", Addr: 0xa000, PointerAddr: 0x3000}}}
	a, err := snapshotgraph.Build(snap, snapshotgraph.Options{KeepProvenance: true})
	if err != nil {
		t.Fatal(err)
	}
	return newSnapshot(a, map[uint64]map[string]string{1: {"tenant": "a", "role": "worker"}, 2: {"tenant": "b"}}, Diagnostics{}, Options{})
}

func TestObjectsClassificationAndDistribution(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	r, err := s.Objects(context.Background(), selector("a"), ObjectOptions{Limit: 2, By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Reachable != (Measure{6, 1112}) || r.Exclusive != (Measure{3, 1040}) || r.Shared != (Measure{3, 72}) || !r.Truncated {
		t.Fatalf("%+v", r)
	}
	if r.Objects[0].ID != 4 || r.Objects[0].Classification != "exclusive" || r.Objects[1].ID != 2 || !r.Objects[1].Global || !r.Objects[1].OtherSystem || !r.Objects[1].OtherGoroutines {
		t.Fatalf("%+v", r.Objects)
	}
	if !reflect.DeepEqual(r.Objects[1].Groups, []string{"a", "b"}) {
		t.Fatal(r.Objects[1])
	}
	var total Measure
	for _, b := range r.Distribution {
		total.Objects += b.Objects
		total.Bytes += b.Bytes
	}
	if total != r.Reachable || len(r.Distribution) != 5 || r.Distribution[0].Min != 0 || r.Distribution[0].Objects != 1 {
		t.Fatalf("%+v", r.Distribution)
	}
	all, err := s.Objects(context.Background(), selector("a"), ObjectOptions{Sort: "address", By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []snapshotgraph.ObjectID{}
	for _, o := range all.Objects {
		ids = append(ids, o.ID)
	}
	if !reflect.DeepEqual(ids, []snapshotgraph.ObjectID{0, 2, 3, 4, 5, 6}) {
		t.Fatal(ids)
	}
	missing, err := s.Objects(context.Background(), selector("none"), ObjectOptions{Limit: 20})
	if err != nil || len(missing.Objects) != 0 || len(missing.Distribution) != 0 || missing.Goroutines != 0 {
		t.Fatalf("%+v %v", missing, err)
	}
}

func TestExplainSharedWitnesses(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	ctx := context.Background()
	r, err := s.Explain(ctx, 0x3004, []Request{selector("a"), selector("b"), selector("absent")}, ExplainOptions{By: "tenant", MaxPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	if r.Object.ID != 2 || r.Object.Classification != "shared" || r.Object.GroupCount != 2 || len(r.Selections) != 3 || r.Selections[2].Reachable || r.PathsTruncated {
		t.Fatalf("%+v", r)
	}
	if r.Paths[0].Root.GoroutineID != 1 || r.Paths[0].Root.Detail != "workerA" || r.Paths[0].Root.Frame.CurrentPC != "0x9000" {
		t.Fatalf("%+v", r.Paths[0])
	}
	if r.Paths[1].Root.GoroutineID != 2 || len(r.Paths[1].Steps) != 2 || r.Paths[1].Steps[0].ID != 1 || r.Paths[1].Steps[1].ID != 2 {
		t.Fatalf("%+v", r.Paths[1])
	}
	step := r.Paths[0].Steps[1]
	if step.IncomingOffset == nil || *step.IncomingOffset != 8 || step.IncomingPointer != "0x3004" {
		t.Fatalf("%+v", step)
	}
	categories := map[string]bool{}
	for _, p := range r.Paths {
		categories[p.Category] = true
		if p.Status != "found" || p.Truncated || p.Steps[len(p.Steps)-1].ID != 2 {
			t.Fatalf("%+v", p)
		}
		// Validate every reported edge, including cyclic and interior-pointer paths.
		for i := 1; i < len(p.Steps); i++ {
			from, to := p.Steps[i-1].ID, p.Steps[i].ID
			found := false
			for _, child := range s.analysis.Graph.Objects[from].Children {
				if child == to {
					found = true
				}
			}
			if !found {
				t.Fatalf("invalid transition %d -> %d", from, to)
			}
		}
	}
	for _, category := range []string{"selection", "global", "other_system", "other_goroutines", "group"} {
		if !categories[category] {
			t.Fatalf("missing %s: %+v", category, r)
		}
	}
	// A zero-length (root directly references target) path still has one object.
	ex, err := s.Explain(ctx, 0x5000, []Request{selector("a"), selector("b")}, ExplainOptions{})
	if err != nil || ex.Object.Classification != "exclusive" || len(ex.Paths) != 1 || len(ex.Paths[0].Steps) != 1 || ex.Selections[1].Reachable {
		t.Fatalf("%+v %v", ex, err)
	}
	notSelected, err := s.Explain(ctx, 0x2000, []Request{selector("a"), selector("b")}, ExplainOptions{})
	if err != nil || notSelected.Object.Classification != "not_reachable_from_selection" || notSelected.Selections[0].Reachable || !notSelected.Selections[1].Reachable {
		t.Fatalf("%+v %v", notSelected, err)
	}
}

func TestExplainBoundsAndAggregate(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	ctx := context.Background()
	r, err := s.Explain(ctx, 0x3000, []Request{selector("a"), selector("b")}, ExplainOptions{By: "tenant", MaxGroups: 1, MaxPaths: 1, MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PathsTruncated || !r.Object.GroupsTruncated || r.Object.GroupCount != 2 || len(r.Paths) != 1 || len(r.Paths[0].Steps) != 1 || !r.Paths[0].Truncated || r.Paths[0].TotalSteps != 2 {
		t.Fatalf("%+v", r)
	}
	r, err = s.Explain(ctx, 0x3000, []Request{selector("a")}, ExplainOptions{MaxVisited: 1})
	if err != nil || r.Paths[0].Status != "search_limit" || !r.Selections[0].Reachable {
		t.Fatalf("%+v %v", r, err)
	}
	aggregate, err := s.ExplainTop(ctx, selector("a"), 2, 3, ExplainOptions{By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Objects) != 2 || len(aggregate.LargestShared) != 2 || len(aggregate.LargestExclusive) != 2 || len(aggregate.Explanations) != 3 || aggregate.LargestShared[0].ID != 2 || aggregate.LargestExclusive[0].ID != 4 || !aggregate.SharedTruncated || !aggregate.ExclusiveTruncated || !aggregate.RepresentativesTruncated {
		t.Fatalf("%+v", aggregate)
	}
	// A huge requested top cannot cause work proportional to an empty limit.
	aggregate, err = s.ExplainTop(ctx, selector("none"), math.MaxInt, 1, ExplainOptions{})
	if err != nil || len(aggregate.Explanations) != 0 {
		t.Fatalf("%+v %v", aggregate, err)
	}
}

func TestInspectionValidationAndOverflow(t *testing.T) {
	s := inspectionFixture(t)
	ctx := context.Background()
	for _, opts := range []ObjectOptions{{Sort: "bad"}, {Limit: -1}, {GroupLimit: -1}} {
		if _, err := s.Objects(ctx, selector("a"), opts); err == nil {
			t.Fatal("invalid options")
		}
	}
	if _, err := s.Explain(ctx, 0x3000, nil, ExplainOptions{}); err == nil {
		t.Fatal("missing selectors")
	}
	if _, err := s.Explain(ctx, 0xbad, []Request{selector("a")}, ExplainOptions{}); err == nil {
		t.Fatal("missing object")
	}
	if _, err := s.Explain(ctx, 0x3000, []Request{selector("a")}, ExplainOptions{MaxSteps: -1}); err == nil {
		t.Fatal("invalid bounds")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Objects(canceled, selector("a"), ObjectOptions{}); err != context.Canceled {
		t.Fatal(err)
	}
	s.analysis.Graph.Objects[4].Size = math.MaxUint64
	if _, err := s.Objects(ctx, selector("a"), ObjectOptions{}); err == nil {
		t.Fatal("overflow accepted")
	}
	s.diag.FailedGoroutines = 1
	if _, err := s.Explain(ctx, 0x3000, []Request{selector("a")}, ExplainOptions{}); err == nil {
		t.Fatal("decode failure ignored")
	}
	s.Close()
	if _, err := s.ExplainTop(ctx, selector("a"), 2, 1, ExplainOptions{}); err == nil {
		t.Fatal("closed snapshot")
	}
}
