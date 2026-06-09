package snapshotgraph

import (
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// BenchmarkBuildWideObject stresses edge construction for a single object
// with many distinct outgoing pointers (the shape of a large []*T backing
// array). With a per-insert linear dedup scan this is quadratic in the
// fan-out; the epoch-stamped dedup keeps it linear.
func BenchmarkBuildWideObject(b *testing.B) {
	const fanout = 100_000
	objs := make([]heapsnapshot.Object, 0, fanout+1)
	ptrs := make([]uint64, fanout)
	for i := 0; i < fanout; i++ {
		addr := uint64(0x100000 + 16*i)
		objs = append(objs, heapsnapshot.Object{Addr: addr, Size: 16})
		ptrs[i] = addr
	}
	objs = append(objs, heapsnapshot.Object{
		Addr:         0x10,
		Size:         8 * fanout,
		PointerAddrs: ptrs,
	})
	snap := &heapsnapshot.HeapSnapshot{Objects: objs}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a, err := Build(snap, Options{})
		if err != nil {
			b.Fatal(err)
		}
		wide := a.Graph.Objects[len(a.Graph.Objects)-1]
		if got := len(wide.Children); got != fanout {
			b.Fatalf("wide object children = %d, want %d", got, fanout)
		}
	}
}
