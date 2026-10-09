package snapshotgraph

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

func TestProvenanceAndWitness(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{Objects: []heapsnapshot.Object{
		{Addr: 0x1000, Size: 32, PointerAddrs: []uint64{0, 0x2004, 0x2000, 0xbad}, PointerOffsets: []uint64{0, 8, 16, 24}},
		{Addr: 0x2000, Size: 16, PointerAddrs: []uint64{0x1000, 0x3000}, PointerOffsets: []uint64{0, 8}},
		{Addr: 0x3000, Size: 8},
	}, Goroutines: []heapsnapshot.Goroutine{{ID: 42, Frames: []heapsnapshot.StackFrame{{SP: 0x9000, Depth: 2, FuncName: "worker", EntryPC: 0x8000, CurrentPC: 0x8004, PointerAddrs: []uint64{0x1004}, PointerSlots: []uint64{0x9008}}}}}, Globals: []heapsnapshot.Root{{Kind: "bss", Addr: 0x7000, PointerAddr: 0x2000}}}
	a, err := Build(snap, Options{KeepProvenance: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Graph.Objects[0].Children, []ObjectID{1}) {
		t.Fatal(a.Graph.Objects[0].Children)
	}
	refs := a.Graph.References[0]
	if len(refs) != 2 || refs[0].Offset != 8 || !refs[0].OffsetKnown || refs[0].Pointer != 0x2004 || refs[1].Offset != 16 {
		t.Fatalf("%+v", refs)
	}
	roots := a.Goroutines[0].Roots
	if roots[0].Frame == nil || roots[0].Frame.CurrentPC != 0x8004 || roots[0].Detail != "worker" || roots[0].SlotAddr != 0x9008 {
		t.Fatalf("%+v", roots)
	}
	w, err := FindPath(context.Background(), a.Graph, roots, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Found || w.RootIndex != 0 || !reflect.DeepEqual(w.Objects, []ObjectID{0, 1, 2}) || len(w.References) != 2 || w.References[0].Pointer != 0x2004 {
		t.Fatalf("%+v", w)
	}
	w, err = FindPath(context.Background(), a.Graph, a.Globals.Roots, 2, 0)
	if err != nil || !reflect.DeepEqual(w.Objects, []ObjectID{1, 2}) {
		t.Fatalf("%+v %v", w, err)
	}
	plain, err := Build(snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Graph.References != nil || plain.Goroutines[0].Roots[0].Frame != nil {
		t.Fatal("unexpected optional metadata")
	}
	w, err = FindPath(context.Background(), plain.Graph, roots, 2, 0)
	if err != nil || !w.Found || w.References[0].OffsetKnown {
		t.Fatalf("%+v %v", w, err)
	}
}

func TestPathLimitsAndInvalidIDs(t *testing.T) {
	g := &Graph{Objects: []Object{{Children: []ObjectID{1, math.MaxUint32}}, {Children: []ObjectID{0, 2}}, {}, {}}}
	roots := []RootRef{{ObjectID: math.MaxUint32}, {ObjectID: 0}, {ObjectID: 0}}
	w, err := FindPath(context.Background(), g, roots, 2, 2)
	if err != nil || w.Found || !w.SearchLimited {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = FindPath(context.Background(), g, roots, 3, 0)
	if err != nil || w.Found || w.SearchLimited {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = FindPath(context.Background(), g, roots, 0, 0)
	if err != nil || !reflect.DeepEqual(w.Objects, []ObjectID{0}) || w.RootIndex != 1 {
		t.Fatalf("%+v %v", w, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = FindPath(ctx, g, roots, 2, 0); err != context.Canceled {
		t.Fatal(err)
	}
	for _, id := range []ObjectID{math.MaxUint32, 4} {
		w, err = FindPath(context.Background(), g, roots, id, 0)
		if err != nil || w.Found {
			t.Fatalf("%+v %v", w, err)
		}
	}
}

func TestLongWitnessIsIterativeAndSearchBounded(t *testing.T) {
	const n = 50000
	g := &Graph{Objects: make([]Object, n)}
	for i := 0; i < n-1; i++ {
		g.Objects[i].Children = []ObjectID{ObjectID(i + 1)}
	}
	roots := []RootRef{{ObjectID: 0}}
	w, err := FindPath(context.Background(), g, roots, n-1, 64)
	if err != nil || w.Found || !w.SearchLimited {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = FindPath(context.Background(), g, roots, n-1, 0)
	if err != nil || !w.Found || len(w.Objects) != n || w.Objects[n-1] != n-1 {
		t.Fatalf("found=%t length=%d err=%v", w.Found, len(w.Objects), err)
	}
}
