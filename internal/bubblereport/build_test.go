package bubblereport

import (
	"testing"

	"bubblepprof/internal/snapshotgraph"
)

func mkObj(id snapshotgraph.ObjectID, addr, size uint64) snapshotgraph.Object {
	return snapshotgraph.Object{ID: id, Addr: addr, Size: size}
}

func mkAnalysis(objects []snapshotgraph.Object, gs []snapshotgraph.GoroutineReachability, global map[snapshotgraph.ObjectID]struct{}) *snapshotgraph.Analysis {
	g := &snapshotgraph.Graph{Objects: objects, ByAddr: map[uint64]snapshotgraph.ObjectID{}}
	for _, o := range objects {
		g.ByAddr[o.Addr] = o.ID
	}
	return &snapshotgraph.Analysis{
		Graph:      g,
		Goroutines: gs,
		Globals:    snapshotgraph.GlobalReachability{Reachable: global},
	}
}

func TestBubbleOneGoroutineReachable(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 100), mkObj(1, 0x200, 50)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}, 1: {}}},
		},
		map[snapshotgraph.ObjectID]struct{}{},
	)
	labels := map[uint64]map[string]string{1: {"bubble": "alpha"}}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Groups) != 1 {
		t.Fatalf("groups = %d", len(r.Groups))
	}
	bubble := r.Groups[0].Bubbles[0]
	if bubble.ReachableBytes != 150 {
		t.Fatalf("reachable bytes = %d", bubble.ReachableBytes)
	}
	if bubble.ExclusiveBytes != 150 {
		t.Fatalf("exclusive bytes = %d", bubble.ExclusiveBytes)
	}
	if bubble.SharedBytes != 0 {
		t.Fatalf("shared bytes = %d", bubble.SharedBytes)
	}
}

func TestBubblesSharedExclusive(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{
			mkObj(0, 0x100, 10),
			mkObj(1, 0x110, 20),
			mkObj(2, 0x130, 40), // shared
		},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}, 2: {}}},
			{GoroutineID: 2, Reachable: map[snapshotgraph.ObjectID]struct{}{1: {}, 2: {}}},
		},
		nil,
	)
	labels := map[uint64]map[string]string{
		1: {"bubble": "alpha"},
		2: {"bubble": "beta"},
	}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	group := r.Groups[0]
	if len(group.Bubbles) != 2 {
		t.Fatalf("bubbles = %d", len(group.Bubbles))
	}
	alpha, beta := group.Bubbles[0], group.Bubbles[1]
	if alpha.Value != "alpha" || beta.Value != "beta" {
		t.Fatalf("order = %s/%s", alpha.Value, beta.Value)
	}
	if alpha.ExclusiveBytes != 10 {
		t.Fatalf("alpha exclusive = %d", alpha.ExclusiveBytes)
	}
	if alpha.SharedBytes != 40 {
		t.Fatalf("alpha shared = %d", alpha.SharedBytes)
	}
	if beta.ExclusiveBytes != 20 {
		t.Fatalf("beta exclusive = %d", beta.ExclusiveBytes)
	}
	if beta.SharedBytes != 40 {
		t.Fatalf("beta shared = %d", beta.SharedBytes)
	}
	if group.SharedBytes != 40 {
		t.Fatalf("group shared = %d", group.SharedBytes)
	}
}

func TestGoroutineWithTwoLabelsBelongsToTwoGroups(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 8)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}}},
		},
		nil,
	)
	labels := map[uint64]map[string]string{
		1: {"bubble": "alpha", "tenant": "acme"},
	}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Groups) != 2 {
		t.Fatalf("groups = %d", len(r.Groups))
	}
	// Groups sorted by key: bubble, tenant.
	if r.Groups[0].Key != "bubble" || r.Groups[1].Key != "tenant" {
		t.Fatalf("group keys = %s/%s", r.Groups[0].Key, r.Groups[1].Key)
	}
}

func TestSystemGoroutineIgnoredByDefault(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 100)},
		[]snapshotgraph.GoroutineReachability{
			{
				GoroutineID: 1,
				IsSystem:    true,
				Reachable:   map[snapshotgraph.ObjectID]struct{}{0: {}},
			},
		},
		nil,
	)
	labels := map[uint64]map[string]string{1: {"bubble": "alpha"}}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Groups) != 0 {
		t.Fatalf("expected no bubbles when only system goroutines are present, got %d groups", len(r.Groups))
	}
	if r.Diagnostics.IgnoredSystemGoroutines != 1 {
		t.Fatalf("IgnoredSystemGoroutines = %d", r.Diagnostics.IgnoredSystemGoroutines)
	}
}

func TestUnlabeledUserGoroutineCountedInDiagnostics(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 100)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}}},
		},
		nil,
	)
	r, err := Build(Input{Analysis: a, LabelsByGID: map[uint64]map[string]string{}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Diagnostics.UnlabeledUserGoroutines != 1 {
		t.Fatalf("UnlabeledUserGoroutines = %d", r.Diagnostics.UnlabeledUserGoroutines)
	}
	if len(r.Groups) != 0 {
		t.Fatalf("expected no groups by default, got %d", len(r.Groups))
	}
}

func TestIncludeUnlabeledCreatesBucket(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 100)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}}},
		},
		nil,
	)
	r, err := Build(Input{
		Analysis:    a,
		LabelsByGID: map[uint64]map[string]string{},
		Options:     Options{IncludeUnlabeled: true},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Groups) != 1 || r.Groups[0].Bubbles[0].Value != "<unlabeled>" {
		t.Fatalf("unexpected groups: %+v", r.Groups)
	}
	if r.Groups[0].Bubbles[0].ReachableBytes != 100 {
		t.Fatalf("reachable bytes = %d", r.Groups[0].Bubbles[0].ReachableBytes)
	}
}

func TestGlobalOverlap(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 10), mkObj(1, 0x110, 20)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}, 1: {}}},
		},
		map[snapshotgraph.ObjectID]struct{}{1: {}},
	)
	labels := map[uint64]map[string]string{1: {"bubble": "alpha"}}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b := r.Groups[0].Bubbles[0]
	if b.GlobalOverlapBytes != 20 || b.GlobalOverlapObjects != 1 {
		t.Fatalf("global overlap = (%d, %d)", b.GlobalOverlapObjects, b.GlobalOverlapBytes)
	}
}

func TestSystemOverlap(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 10), mkObj(1, 0x110, 20)},
		[]snapshotgraph.GoroutineReachability{
			// User goroutine reaches obj 0 and 1.
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}, 1: {}}},
			// System goroutine reaches obj 1 only.
			{GoroutineID: 2, IsSystem: true, Reachable: map[snapshotgraph.ObjectID]struct{}{1: {}}},
		},
		nil,
	)
	labels := map[uint64]map[string]string{1: {"bubble": "alpha"}}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b := r.Groups[0].Bubbles[0]
	if b.SystemOverlapObjects != 1 || b.SystemOverlapBytes != 20 {
		t.Fatalf("system overlap = (%d, %d)", b.SystemOverlapObjects, b.SystemOverlapBytes)
	}
}

func TestLabelKeyFilter(t *testing.T) {
	a := mkAnalysis(
		[]snapshotgraph.Object{mkObj(0, 0x100, 10)},
		[]snapshotgraph.GoroutineReachability{
			{GoroutineID: 1, Reachable: map[snapshotgraph.ObjectID]struct{}{0: {}}},
		},
		nil,
	)
	labels := map[uint64]map[string]string{1: {"bubble": "alpha", "tenant": "acme"}}
	r, err := Build(Input{Analysis: a, LabelsByGID: labels, Options: Options{LabelKeyFilter: "tenant"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Groups) != 1 || r.Groups[0].Key != "tenant" {
		t.Fatalf("filter ignored: %+v", r.Groups)
	}
}
