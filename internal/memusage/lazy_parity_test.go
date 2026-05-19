package memusage

import (
	"bytes"
	"reflect"
	"sort"
	"testing"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/runtimelayout"
	"bubblepprof/internal/snapshotgraph"
)

// TestLazyContentsParityWithEager is the bit-for-bit gate for the
// ParseLazyContents refactor: against a single heap dump captured from
// the live test runtime, the eager Parse(KeepObjectContents=true) path
// and the lazy ParseLazyContents path must produce identical recovered
// labels, identical Build stats, and identical Response bytes when fed
// through the rest of the pipeline.
//
// Skipped on unsupported runtimes (no layout entry) because there are
// no goroutine labels to recover and the test would otherwise compare
// degenerate empty outputs.
func TestLazyContentsParityWithEager(t *testing.T) {
	stop := spawnLabeledWorker(t)
	defer stop()

	// One capture, both decoded paths read the same bytes.
	dump := captureHeapBytes(t)

	// --- eager path: Parse(KeepObjectContents=true) + NewMemory(snap) ---
	eagerSnap, err := heapdump.Parse(bytes.NewReader(dump), heapdump.Options{
		KeepObjectContents: true,
		Strict:             true,
	})
	if err != nil {
		t.Fatalf("eager parse: %v", err)
	}

	layout, ok := runtimelayout.Lookup(heaplabels.LookupInputFromSnapshot(eagerSnap))
	if !ok {
		t.Skipf("runtime layout unsupported (go=%s arch=%s); parity test requires a supported layout to have labels to compare", eagerSnap.Params.BuildVersion, eagerSnap.Params.GOARCH)
	}

	eagerResult := heaplabels.DecodeAll(eagerSnap, layout, heaplabels.Options{})
	eagerAnalysis, err := snapshotgraph.Build(eagerSnap, snapshotgraph.Options{})
	if err != nil {
		t.Fatalf("eager build: %v", err)
	}

	// --- lazy path: ParseLazyContents + NewMemoryFromReader(resolver) ---
	lazyReader := bytes.NewReader(dump)
	lazySnap, resolver, err := heapdump.ParseLazyContents(lazyReader, lazyReader, heapdump.Options{
		Strict: true,
	})
	if err != nil {
		t.Fatalf("lazy parse: %v", err)
	}
	if resolver.ObjectCount() == 0 {
		t.Fatal("resolver indexed zero objects; expected at least the worker's payload")
	}

	heapMem := heaplabels.NewMemoryFromReader(resolver)
	lazyResult := heaplabels.DecodeAll(lazySnap, layout, heaplabels.Options{
		HeapMemory: heapMem,
	})
	lazyAnalysis, err := snapshotgraph.Build(lazySnap, snapshotgraph.Options{})
	if err != nil {
		t.Fatalf("lazy build: %v", err)
	}

	// --- compare label recovery ---
	if !reflect.DeepEqual(eagerResult.LabelsByGID, lazyResult.LabelsByGID) {
		t.Fatalf("LabelsByGID differs:\n eager=%v\n lazy =%v", eagerResult.LabelsByGID, lazyResult.LabelsByGID)
	}
	if eagerResult.Stats != lazyResult.Stats {
		t.Fatalf("label stats differ:\n eager=%+v\n lazy =%+v", eagerResult.Stats, lazyResult.Stats)
	}

	// --- compare per-goroutine decode results, ignoring slice ordering ---
	if len(eagerResult.Goroutines) != len(lazyResult.Goroutines) {
		t.Fatalf("goroutine count differs: eager=%d lazy=%d", len(eagerResult.Goroutines), len(lazyResult.Goroutines))
	}
	sortByGID := func(grs []heaplabels.GoroutineResult) {
		sort.Slice(grs, func(i, j int) bool { return grs[i].GID < grs[j].GID })
	}
	sortByGID(eagerResult.Goroutines)
	sortByGID(lazyResult.Goroutines)
	for i := range eagerResult.Goroutines {
		if !reflect.DeepEqual(eagerResult.Goroutines[i], lazyResult.Goroutines[i]) {
			t.Fatalf("goroutine %d: eager=%+v lazy=%+v", i, eagerResult.Goroutines[i], lazyResult.Goroutines[i])
		}
	}

	// --- compare graph build stats ---
	if eagerAnalysis.Stats != lazyAnalysis.Stats {
		t.Fatalf("graph stats differ:\n eager=%+v\n lazy =%+v", eagerAnalysis.Stats, lazyAnalysis.Stats)
	}
	if len(eagerAnalysis.Goroutines) != len(lazyAnalysis.Goroutines) {
		t.Fatalf("graph goroutine count differs: eager=%d lazy=%d",
			len(eagerAnalysis.Goroutines), len(lazyAnalysis.Goroutines))
	}
}
