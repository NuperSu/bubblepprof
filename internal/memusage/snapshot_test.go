package memusage

import (
	"context"
	"reflect"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

func TestSnapshotRepeatedQueries(t *testing.T) {
	g := &snapshotgraph.Graph{Objects: []snapshotgraph.Object{
		{ID: 0, Size: 10, Children: []snapshotgraph.ObjectID{2}},
		{ID: 1, Size: 20, Children: []snapshotgraph.ObjectID{2}},
		{ID: 2, Size: 30, Children: []snapshotgraph.ObjectID{3}},
		{ID: 3, Size: 40, Children: []snapshotgraph.ObjectID{2}},
	}}
	roots := func(id snapshotgraph.ObjectID) []snapshotgraph.RootRef {
		return []snapshotgraph.RootRef{{ObjectID: id}}
	}
	a := &snapshotgraph.Analysis{Graph: g, Goroutines: []snapshotgraph.GoroutineReachability{
		{GoroutineID: 1, Roots: roots(0)}, {GoroutineID: 2, Roots: roots(1)},
		{GoroutineID: 3, Roots: roots(0)}, {GoroutineID: 4, Roots: roots(3)},
		{GoroutineID: 5, IsSystem: true, Roots: roots(2)},
	}, Globals: snapshotgraph.GlobalReachability{Roots: roots(2)}}
	labels := map[uint64]map[string]string{1: {"tenant": "a", "role": "worker"}, 2: {"tenant": "b"}, 3: {"tenant": "a", "role": "worker"}}
	s := newSnapshot(a, labels, Diagnostics{}, Options{})
	if !reflect.DeepEqual(s.LabelKeys(), []string{"role", "tenant"}) || !reflect.DeepEqual(s.LabelValues("tenant"), []string{"a", "b"}) || !reflect.DeepEqual(s.GoroutineIDs("tenant", "a"), []uint64{1, 3}) {
		t.Fatal("incorrect label index")
	}
	for _, sel := range []map[string]string{{"tenant": "a"}, {"tenant": "b"}, {"tenant": "a", "role": "worker"}, {"tenant": "missing"}, {"tenant": "a", "role": "missing"}, {"tenant": "a"}} {
		req := Request{Labels: sel}
		want, err := ComputeFromAnalysis(req, a, labels, Diagnostics{}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Query(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %+v, want %+v", sel, got, want)
		}
		if s.analysis != a || s.analysis.Graph != g {
			t.Fatal("graph replaced during query")
		}
	}
	if !reflect.DeepEqual(s.cache.global, map[snapshotgraph.ObjectID]struct{}{2: {}, 3: {}}) || !reflect.DeepEqual(s.cache.system, s.cache.global) {
		t.Fatal("incorrect invariant sets")
	}
	if got := reachableFromGoroutines(g, []*snapshotgraph.GoroutineReachability{&a.Goroutines[0], &a.Goroutines[2]}); !reflect.DeepEqual(got, map[snapshotgraph.ObjectID]struct{}{0: {}, 2: {}, 3: {}}) {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Query(ctx, Request{Labels: map[string]string{"tenant": "a"}}); err != context.Canceled {
		t.Fatal(err)
	}
	s.Close()
	s.Close()
	if _, err := s.Query(context.Background(), Request{}); err == nil {
		t.Fatal("query after close")
	}
	if s.analysis != nil || s.labels != nil || s.cache.global != nil {
		t.Fatal("resources retained")
	}
}

func TestSnapshotDecodeFailuresAreNotZeroMatches(t *testing.T) {
	a := &snapshotgraph.Analysis{Graph: &snapshotgraph.Graph{}, Goroutines: []snapshotgraph.GoroutineReachability{{GoroutineID: 1}}}
	s := newSnapshot(a, nil, Diagnostics{FailedGoroutines: 1}, Options{})
	defer s.Close()
	if _, err := s.Query(context.Background(), Request{Labels: map[string]string{"tenant": "absent"}}); err == nil {
		t.Fatal("unreliable labels produced success")
	}
}
