package memusage

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

func multiFixture() *Snapshot {
	// 0 exclusive A; 1 A+B; 2 A+B+C; 3 A+global; 4 A+B+global;
	// 5 A+unlabeled; 6 A+system; 7 exclusive B; 8 exclusive C.
	g := &snapshotgraph.Graph{Objects: make([]snapshotgraph.Object, 9)}
	for i := range g.Objects {
		g.Objects[i] = snapshotgraph.Object{ID: snapshotgraph.ObjectID(i), Addr: 0x100000000 + uint64(i)*32, Size: uint64(i + 1)}
	}
	g.Objects[1].Children = []snapshotgraph.ObjectID{2}
	g.Objects[2].Children = []snapshotgraph.ObjectID{1} // cycle
	roots := func(ids ...snapshotgraph.ObjectID) []snapshotgraph.RootRef {
		out := []snapshotgraph.RootRef{}
		for _, id := range ids {
			out = append(out, snapshotgraph.RootRef{ObjectID: id})
		}
		return out
	}
	a := &snapshotgraph.Analysis{Graph: g, Goroutines: []snapshotgraph.GoroutineReachability{
		{GoroutineID: 1, Roots: roots(0, 1, 3, 4, 5, 6)},
		{GoroutineID: 2, Roots: roots(1, 4, 7)},
		{GoroutineID: 3, Roots: roots(2, 8)},
		{GoroutineID: 4, Roots: roots(5)},
		{GoroutineID: 5, IsSystem: true, Roots: roots(6)},
		{GoroutineID: 6, Roots: roots(0)}, // same label, duplicate reach
	}, Globals: snapshotgraph.GlobalReachability{Roots: roots(3, 4)}}
	// The cycle makes both 1 and 2 shared by three groups.
	return newSnapshot(a, map[uint64]map[string]string{1: {"tenant": "a", "role": "worker"}, 2: {"tenant": "b"}, 3: {"tenant": "c"}, 6: {"tenant": "a"}}, Diagnostics{}, Options{})
}
func selector(v string) Request { return Request{Labels: map[string]string{"tenant": v}} }
func TestMultiBubbleClassification(t *testing.T) {
	s := multiFixture()
	defer s.Close()
	ctx := context.Background()
	top, err := s.Top(ctx, TopOptions{By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if top.TotalGroups != 3 || top.UngroupedGoroutines != 1 || top.ExcludedSystemGoroutines != 1 {
		t.Fatalf("%+v", top)
	}
	a := top.Groups[0]
	if a.Value != "a" || a.Goroutines != 2 {
		t.Fatalf("%+v", a)
	}
	want := Breakdown{Reachable: Measure{7, 28}, Exclusive: Measure{1, 1}, Shared: Measure{6, 27}, BubblesOnly: Measure{2, 5}, OtherRootsOnly: Measure{3, 17}, Both: Measure{1, 5}, BubbleOverlap: Measure{3, 10}, OtherRootOverlap: Measure{4, 22}, GlobalOverlap: Measure{2, 9}, SystemOverlap: Measure{1, 7}, UngroupedOverlap: Measure{1, 6}}
	if !reflect.DeepEqual(a.Breakdown, want) {
		t.Fatalf("got %+v want %+v", a.Breakdown, want)
	}
	for _, row := range top.Groups {
		if row.Reachable.Bytes != row.Exclusive.Bytes+row.BubblesOnly.Bytes+row.OtherRootsOnly.Bytes+row.Both.Bytes {
			t.Fatalf("partition: %+v", row)
		}
	}
	ex, err := s.Exclusive(ctx, selector("a"))
	if err != nil {
		t.Fatal(err)
	}
	if ex.Exclusive != a.Exclusive || ex.Shared != a.Shared || ex.OtherGoroutineOverlap != (Measure{4, 16}) {
		t.Fatalf("%+v", ex)
	}
	shared, err := s.Shared(ctx, selector("a"), selector("b"), 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []snapshotgraph.ObjectID{}
	for _, o := range shared.Objects {
		ids = append(ids, o.ID)
		if o.Address == "" {
			t.Fatal("missing address")
		}
	}
	if !reflect.DeepEqual(ids, []snapshotgraph.ObjectID{4, 2, 1}) {
		t.Fatalf("shared IDs %v", ids)
	}
	if shared.AOnly != (Measure{4, 18}) || shared.BOnly != (Measure{1, 8}) || shared.Shared != (Measure{3, 10}) || shared.Union != (Measure{8, 36}) {
		t.Fatalf("%+v", shared)
	}
	limited, err := s.Shared(ctx, selector("a"), selector("b"), 1)
	if err != nil || !limited.Truncated || len(limited.Objects) != 1 || limited.Shared != shared.Shared {
		t.Fatalf("%+v %v", limited, err)
	}
	// Different selectors select the same root; pairwise differences stay exact.
	c, err := s.Compare(ctx, selector("a"), Request{Labels: map[string]string{"role": "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.OverlappingGoroutines, []uint64{1}) || c.AOnly.Objects != 0 || c.BOnly.Objects != 0 || c.Shared != (Measure{7, 28}) {
		t.Fatalf("%+v", c)
	}
	c, err = s.Compare(ctx, selector("absent"), selector("b"))
	if err != nil || c.AOnly.Objects != 0 || c.Shared.Objects != 0 || c.BOnly != (Measure{4, 18}) {
		t.Fatalf("%+v %v", c, err)
	}
	ranked, err := s.Top(ctx, TopOptions{By: "tenant", Sort: "exclusive", Limit: 1})
	if err != nil || ranked.Groups[0].Value != "c" || ranked.TotalGroups != 3 {
		t.Fatalf("%+v %v", ranked, err)
	}
	ranked, err = s.Top(ctx, TopOptions{By: "tenant", Sort: "shared", Limit: 1})
	if err != nil || ranked.Groups[0].Shared != a.Shared {
		t.Fatalf("limit changed classification %+v %v", ranked, err)
	}
}

func TestMultiBubbleSystemAndMissingLabels(t *testing.T) {
	s := multiFixture()
	s.opts.IncludeSystemGoroutines = true
	s.labels[5] = map[string]string{"tenant": "system"}
	s.labels[4] = map[string]string{"tenant": ""}
	r, err := s.Top(context.Background(), TopOptions{By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TotalGroups != 5 || r.UngroupedGoroutines != 0 || r.ExcludedSystemGoroutines != 0 {
		t.Fatalf("%+v", r)
	}
	for _, row := range r.Groups {
		if row.Value == "a" && (row.SystemOverlap.Objects != 0 || row.BubbleOverlap.Objects != 5) {
			t.Fatalf("%+v", row)
		}
	}
	r, err = s.Top(context.Background(), TopOptions{By: "missing"})
	if err != nil || len(r.Groups) != 0 || r.UngroupedGoroutines != 6 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestMultiBubbleValidation(t *testing.T) {
	s := multiFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Top(ctx, TopOptions{By: "tenant"}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := s.Compare(context.Background(), Request{}, selector("a")); err == nil {
		t.Fatal("accepted empty selector")
	}
	if _, err := s.Top(context.Background(), TopOptions{By: "tenant", Sort: "bad"}); err == nil {
		t.Fatal("accepted sort")
	}
	if _, err := s.Shared(context.Background(), selector("a"), selector("b"), -1); err == nil {
		t.Fatal("accepted limit")
	}
	s.diag.FailedGoroutines = 1
	if _, err := s.Top(context.Background(), TopOptions{By: "tenant"}); err == nil {
		t.Fatal("silently ignored decoding failure")
	}
	s.diag = Diagnostics{}
	s.analysis.Graph.Objects[0].Size = math.MaxUint64
	if _, err := s.Exclusive(context.Background(), selector("a")); err == nil {
		t.Fatal("accepted byte overflow")
	}
	s.Close()
	if _, err := s.Exclusive(context.Background(), selector("a")); err == nil {
		t.Fatal("accepted closed snapshot")
	}
}

func TestMultiBubbleThousandsOfGroups(t *testing.T) {
	const n = 3000
	graph := &snapshotgraph.Graph{Objects: make([]snapshotgraph.Object, n+1)}
	graph.Objects[n] = snapshotgraph.Object{ID: n, Size: 11}
	a := &snapshotgraph.Analysis{Graph: graph}
	labels := map[uint64]map[string]string{}
	for i := 0; i < n; i++ {
		id := snapshotgraph.ObjectID(i)
		graph.Objects[i] = snapshotgraph.Object{ID: id, Size: 7, Children: []snapshotgraph.ObjectID{n}}
		gid := uint64(i + 1)
		a.Goroutines = append(a.Goroutines, snapshotgraph.GoroutineReachability{GoroutineID: gid, Roots: []snapshotgraph.RootRef{{ObjectID: id}}})
		labels[gid] = map[string]string{"group": fmtGroup(i)}
	}
	s := newSnapshot(a, labels, Diagnostics{}, Options{})
	defer s.Close()
	r, err := s.Top(context.Background(), TopOptions{By: "group"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != n {
		t.Fatal(len(r.Groups))
	}
	for _, row := range r.Groups {
		if row.Reachable != (Measure{2, 18}) || row.Exclusive != (Measure{1, 7}) || row.BubblesOnly != (Measure{1, 11}) {
			t.Fatalf("%+v", row)
		}
	}
}
func fmtGroup(i int) string { // deterministic unique values without a platform-sized address conversion
	return string(rune(i + 100))
}

func TestMultiBubbleExactSetsAndMalformedReferences(t *testing.T) {
	s := multiFixture()
	defer s.Close()
	ctx := context.Background()
	s.analysis.Graph.Objects[0].Children = []snapshotgraph.ObjectID{0, snapshotgraph.ObjectID(math.MaxUint32)}
	ar, _, err := s.selected(selector("a"))
	if err != nil {
		t.Fatal(err)
	}
	br, _, err := s.selected(selector("b"))
	if err != nil {
		t.Fatal(err)
	}
	ar = append(ar, snapshotgraph.RootRef{ObjectID: snapshotgraph.ObjectID(math.MaxUint32)})
	as, err := s.walk(ctx, ar)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := s.walk(ctx, br)
	if err != nil {
		t.Fatal(err)
	}
	want := func(ids ...snapshotgraph.ObjectID) objectSet {
		out := objectSet{}
		for _, id := range ids {
			out[id] = struct{}{}
		}
		return out
	}
	for _, tc := range []struct {
		name      string
		got, want objectSet
	}{
		{"A", as, want(0, 1, 2, 3, 4, 5, 6)},
		{"B", bs, want(1, 2, 4, 7)},
		{"A-only", difference(as, bs), want(0, 3, 5, 6)},
		{"B-only", difference(bs, as), want(7)},
		{"intersection", intersection(as, bs), want(1, 2, 4)},
		{"union", union(as, bs), want(0, 1, 2, 3, 4, 5, 6, 7)},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Fatalf("%s: %v want %v", tc.name, tc.got, tc.want)
		}
	}
	roots := append([]snapshotgraph.RootRef{}, s.analysis.Globals.Roots...)
	for _, g := range s.analysis.Goroutines {
		if g.GoroutineID != 1 && g.GoroutineID != 6 {
			roots = append(roots, g.Roots...)
		}
	}
	outside, err := s.walk(ctx, roots)
	if err != nil {
		t.Fatal(err)
	}
	if got := difference(as, outside); !reflect.DeepEqual(got, want(0)) {
		t.Fatalf("exclusive set: %v", got)
	}
}

func TestIncludedUngroupedSystemOverlap(t *testing.T) {
	s := multiFixture()
	defer s.Close()
	s.opts.IncludeSystemGoroutines = true
	r, err := s.Top(context.Background(), TopOptions{By: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if r.UngroupedGoroutines != 2 || r.ExcludedSystemGoroutines != 0 {
		t.Fatalf("%+v", r)
	}
	for _, g := range r.Groups {
		if g.Value == "a" && (g.SystemOverlap != (Measure{1, 7}) || g.UngroupedOverlap != (Measure{2, 13})) {
			t.Fatalf("%+v", g)
		}
	}
}
