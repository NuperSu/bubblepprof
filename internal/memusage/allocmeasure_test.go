package memusage

import (
	"context"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestMeasureQueryAllocation answers one precise question: for a single
// /debug/memusage call against a realistic resident heap, how many bytes does
// the call ALLOCATE (TotalAlloc delta = transient churn) versus how much live
// heap does it RETAIN once the next GC runs (HeapAlloc delta)?
//
// Those are two different numbers and conflating them is the whole confusion:
//
//   - TotalAlloc delta is cumulative-allocated bytes; it never decreases, so it
//     captures every temporary buffer the parser/graph builder churned through.
//     This is the "~1x heap" figure the cold cmd/bench reports.
//   - HeapAlloc-after-GC delta is what stays LIVE after the call returns and the
//     transient garbage is collected. If the endpoint leaks nothing, it is ~0.
//
// The workload here is idle (built once, then quiescent) on purpose: with no
// concurrent allocation, TotalAlloc delta is the PURE endpoint cost, not the
// workload's own churn. This isolates allocation; it says nothing about RSS
// (which is regime-dependent and measured separately by
// examples/log_ingest/measure_overhead.sh on a warm server).
//
// Gated behind an env var because it allocates several GiB and is a
// measurement, not a pass/fail assertion:
//
//	MEMUSAGE_ALLOC_MEASURE=1 go test ./internal/memusage \
//	    -run TestMeasureQueryAllocation -v -timeout 30m
func TestMeasureQueryAllocation(t *testing.T) {
	if os.Getenv("MEMUSAGE_ALLOC_MEASURE") == "" {
		t.Skip("set MEMUSAGE_ALLOC_MEASURE=1 to run the allocation measurement")
	}

	ctx := context.Background()
	// Mirror the real endpoint: GCBeforeHeapDump defaults true in
	// pkg/bubblepprof.MemUsageHandler.
	comp := NewComputer(Options{GCBeforeHeapDump: true})
	defer comp.Close()

	const goroutines = 200
	sizesMB := []int{128, 256, 512, 1024}

	t.Logf("%-9s %-12s %-12s %-14s %-16s %-14s",
		"heap_MiB", "reachable", "totalAlloc", "totalAlloc/heap", "heapAlloc_imm", "retained_GC")
	for _, mb := range sizesMB {
		measureOne(t, ctx, comp, mb, goroutines)
	}
}

func measureOne(t *testing.T, ctx context.Context, comp *Computer, heapMB, goroutines int) {
	t.Helper()

	wctx, cancel := context.WithCancel(ctx)
	done := buildResidentHeap(wctx, heapMB, goroutines)
	defer func() { cancel(); done() }()

	// Settle the live heap and run any queued finalizers so the baseline is
	// stable and the transient garbage from building the workload is gone.
	runtime.GC()
	runtime.GC()

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	resp, err := comp.Compute(ctx, Request{Labels: map[string]string{"job": "alpha"}})
	if err != nil {
		t.Fatalf("heap=%d MiB: Compute: %v", heapMB, err)
	}

	// Read immediately: TotalAlloc is monotonic so its delta is the exact
	// number of bytes the call allocated, regardless of any GC that ran
	// mid-call. HeapAlloc here is the post-internal-GC live size, an upper
	// bound that still includes not-yet-collected transient objects.
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// Now collect the transient garbage and measure what actually STAYS live.
	runtime.GC()
	runtime.GC()
	var settled runtime.MemStats
	runtime.ReadMemStats(&settled)

	totalAllocDelta := after.TotalAlloc - before.TotalAlloc
	heapAllocImmediate := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	retained := int64(settled.HeapAlloc) - int64(before.HeapAlloc)

	const mib = 1 << 20
	ratio := float64(totalAllocDelta) / float64(before.HeapAlloc)

	t.Logf("%-9d %-12d %-12.1f %-14.2fx %-16.1f %-14.1f  (matched=%d, baseline heap=%.1f MiB)",
		heapMB,
		resp.ReachableBytes,
		float64(totalAllocDelta)/mib,
		ratio,
		float64(heapAllocImmediate)/mib,
		float64(retained)/mib,
		resp.MatchedGoroutines,
		float64(before.HeapAlloc)/mib,
	)
}

// buildResidentHeap spawns labeled goroutines that each retain a slice of heap
// (touched so it is real) until the context is cancelled. Label bytes are
// heap-allocated via strings.Clone so heap-native decoding works without
// depending on the in-process address-space reader. Mirrors cmd/bench's
// spawnWorkload but trimmed to what the measurement needs.
func buildResidentHeap(ctx context.Context, heapMB, goroutines int) func() {
	totalBytes := int64(heapMB) << 20
	perWorker := totalBytes / int64(goroutines)
	if perWorker < 1024 {
		perWorker = 1024
	}

	jobKey := strings.Clone("job")
	jobVal := strings.Clone("alpha")

	started := make(chan struct{}, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		shardKey := strings.Clone("shard")
		shardVal := strings.Clone(strconv.Itoa(i))
		size := perWorker
		pprof.Do(ctx, pprof.Labels(jobKey, jobVal, shardKey, shardVal), func(ctx context.Context) {
			go func() {
				defer wg.Done()
				pprof.SetGoroutineLabels(ctx)
				data := make([]byte, size)
				for off := int64(0); off < size; off += 4096 {
					data[off] = byte(off)
				}
				started <- struct{}{}
				<-ctx.Done()
				runtime.KeepAlive(data)
			}()
		})
	}
	for i := 0; i < goroutines; i++ {
		<-started
	}
	return func() { wg.Wait() }
}
