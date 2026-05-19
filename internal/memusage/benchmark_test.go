package memusage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshotgraph"
)

// benchHeapMB is the resident heap target used by the fixture-based
// benchmarks (parse / build / labels / reachable / end-to-end). Override
// with -bench-heap-mb. The live-capture WriteHeapDump benchmark sweeps
// sizes via sub-benchmarks and ignores this knob.
const benchHeapMB = 100

// retainedHeap allocates and touches `mb` MiB of byte slices and returns a
// closure that, when called, keeps them alive across the benchmark. Pages
// are touched so the OS commits real RSS rather than COW zero pages.
func retainedHeap(tb testing.TB, mb int) func() {
	tb.Helper()
	if mb <= 0 {
		return func() {}
	}
	const chunk = 1 << 20
	blobs := make([][]byte, mb)
	for i := range blobs {
		blobs[i] = make([]byte, chunk)
		for j := 0; j < chunk; j += 4096 {
			blobs[i][j] = byte(i + j)
		}
	}
	runtime.GC()
	return func() { runtime.KeepAlive(blobs) }
}

// spawnLabeledWorker launches one goroutine carrying heap-allocated
// pprof labels and a small retained payload. The labels are constructed
// with strings.Clone so the bytes live in heap object contents and the
// label decoder does not depend on the in-process address-space reader.
func spawnLabeledWorker(tb testing.TB) func() {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	k := strings.Clone("job")
	v := strings.Clone("bench")
	pprof.Do(ctx, pprof.Labels(k, v), func(ctx context.Context) {
		go func() {
			defer wg.Done()
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, 1<<20)
			for i := 0; i < len(data); i += 4096 {
				data[i] = byte(i)
			}
			close(started)
			<-ctx.Done()
			runtime.KeepAlive(data)
		}()
	})
	<-started
	return func() {
		cancel()
		wg.Wait()
	}
}

func captureHeapBytes(tb testing.TB) []byte {
	tb.Helper()
	c, err := capture.Capture(context.Background(), capture.CaptureOptions{})
	if err != nil {
		tb.Fatalf("capture heap dump: %v", err)
	}
	defer c.Cleanup()
	buf, err := io.ReadAll(c.HeapDump)
	if err != nil {
		tb.Fatalf("read heap dump: %v", err)
	}
	return buf
}

func parseHeapBytes(tb testing.TB, buf []byte) *heapsnapshot.HeapSnapshot {
	tb.Helper()
	snap, err := heapdump.Parse(bytes.NewReader(buf), heapdump.Options{KeepObjectContents: true, Strict: true})
	if err != nil {
		tb.Fatalf("parse heap dump: %v", err)
	}
	return snap
}

func BenchmarkParse(b *testing.B) {
	keep := retainedHeap(b, benchHeapMB)
	defer keep()
	buf := captureHeapBytes(b)
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := heapdump.Parse(bytes.NewReader(buf), heapdump.Options{KeepObjectContents: true, Strict: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildGraph(b *testing.B) {
	keep := retainedHeap(b, benchHeapMB)
	defer keep()
	snap := parseHeapBytes(b, captureHeapBytes(b))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := snapshotgraph.Build(snap, snapshotgraph.Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecoverLabels(b *testing.B) {
	keep := retainedHeap(b, benchHeapMB)
	defer keep()
	stop := spawnLabeledWorker(b)
	defer stop()
	snap := parseHeapBytes(b, captureHeapBytes(b))
	rec := DefaultLabelRecoverer{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rec.Recover(snap, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReachableFromGoroutines(b *testing.B) {
	keep := retainedHeap(b, benchHeapMB)
	defer keep()
	snap := parseHeapBytes(b, captureHeapBytes(b))
	analysis, err := snapshotgraph.Build(snap, snapshotgraph.Options{})
	if err != nil {
		b.Fatal(err)
	}
	matched := make([]*snapshotgraph.GoroutineReachability, 0, len(analysis.Goroutines))
	for i := range analysis.Goroutines {
		gr := &analysis.Goroutines[i]
		if gr.IsSystem || gr.IsBackground {
			continue
		}
		matched = append(matched, gr)
	}
	if len(matched) == 0 {
		b.Skip("no non-system goroutines in fixture")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reachableFromGoroutines(analysis.Graph, matched)
	}
}

func BenchmarkComputeEndToEnd(b *testing.B) {
	keep := retainedHeap(b, benchHeapMB)
	defer keep()
	stop := spawnLabeledWorker(b)
	defer stop()
	comp := NewComputer(Options{})
	defer comp.Close()
	req := Request{Labels: map[string]string{"job": "bench"}}
	if _, err := comp.Compute(context.Background(), req); err != nil {
		var unsupported *UnsupportedRuntimeError
		if errors.As(err, &unsupported) {
			b.Skipf("unsupported runtime layout for live heap-native label decoding: %v", err)
		}
		b.Fatalf("warmup compute: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := comp.Compute(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteHeapDump(b *testing.B) {
	for _, mb := range []int{50, 200, 500} {
		mb := mb
		b.Run(fmt.Sprintf("heap=%dMB", mb), func(b *testing.B) {
			keep := retainedHeap(b, mb)
			defer keep()
			runtime.GC()
			tmp, err := os.CreateTemp("", "bubblepprof-bench-heap-*.dump")
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
			}()
			b.ReportAllocs()
			var totalBytes int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if _, err := tmp.Seek(0, io.SeekStart); err != nil {
					b.Fatal(err)
				}
				if err := tmp.Truncate(0); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				debug.WriteHeapDump(tmp.Fd())
				b.StopTimer()
				info, err := tmp.Stat()
				if err != nil {
					b.Fatal(err)
				}
				totalBytes += info.Size()
				b.StartTimer()
			}
			b.StopTimer()
			if b.N > 0 {
				b.SetBytes(totalBytes / int64(b.N))
			}
		})
	}
}
