package memusage

import (
	"context"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
	"reflect"
	"testing"
)

func TestSummarySetsAndStructure(t *testing.T) {
	s := multiFixture()
	defer s.Close()
	s.analysis.Goroutines[0].Roots[0].Kind = "stack"
	s.analysis.Goroutines[0].Roots[0].Detail = "main.worker"
	in, err := s.inspect(context.Background(), selector("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(difference(in.selected, in.outside), objectSet{snapshotgraph.ObjectID(0): {}}) {
		t.Fatal("exclusive set")
	}
	r, err := s.Summary(context.Background(), selector("a"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics["reachable"] != (Measure{7, 28}) || r.Metrics["exclusive"] != (Measure{1, 1}) || r.Metrics["shared"] != (Measure{6, 27}) || r.Metrics["global_overlap"] != (Measure{2, 9}) || r.Metrics["system_overlap"] != (Measure{1, 7}) {
		t.Fatalf("%+v", r)
	}
	if r.Roots != 7 || r.RootFunctions["main.worker"] != 1 || r.RootCategories["stack"] != 1 {
		t.Fatalf("%+v", r)
	}
	var m Measure
	for _, b := range r.Distribution {
		m.Objects += b.Objects
		m.Bytes += b.Bytes
	}
	if m != r.Metrics["reachable"] {
		t.Fatal(m)
	}
	// A labeled goroutine with no resolved roots is present, with zero memory.
	s.analysis.Goroutines[1].Roots = nil
	z, err := s.Summary(context.Background(), selector("b"))
	if err != nil || !z.Present || z.Metrics["reachable"].Objects != 0 {
		t.Fatalf("%+v %v", z, err)
	}
	absent, err := s.Summary(context.Background(), selector("missing"))
	if err != nil || absent.Present {
		t.Fatalf("%+v %v", absent, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Summary(ctx, selector("a")); err == nil {
		t.Fatal("cancellation")
	}
	s.Close()
	if _, err := s.Summary(context.Background(), selector("a")); err == nil {
		t.Fatal("closed")
	}
}
func TestSummaryConstantUnionIncreasingSharing(t *testing.T) {
	s := multiFixture()
	defer s.Close()
	ctx := context.Background()
	before, err := s.Compare(ctx, selector("a"), selector("b"))
	if err != nil {
		t.Fatal(err)
	}
	// B adds a root to the previously exclusive A object. The union is unchanged.
	s.analysis.Goroutines[1].Roots = append(s.analysis.Goroutines[1].Roots, snapshotgraph.RootRef{ObjectID: 0})
	after, err := s.Compare(ctx, selector("a"), selector("b"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Union != before.Union || after.Shared.Bytes != before.Shared.Bytes+1 {
		t.Fatalf("%+v %+v", before, after)
	}
	r, err := s.Summary(ctx, selector("a"))
	if err != nil || r.Metrics["exclusive"].Objects != 0 || r.Metrics["shared"].Bytes != 28 {
		t.Fatalf("%+v %v", r, err)
	}
}
