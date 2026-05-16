package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	key := dynamicString("bubble")
	value := dynamicString("alpha")
	jobKey := dynamicString("job")
	jobValue := dynamicString("42")

	ready := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)

	ctx := context.Background()
	pprof.Do(ctx, pprof.Labels(key, value, jobKey, jobValue), func(ctx context.Context) {
		go func() {
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, 4<<20)
			close(ready)
			<-stop
			runtime.KeepAlive(data)
		}()
		<-ready
	})

	captured, err := capture.Capture(context.Background(), capture.CaptureOptions{
		GCBeforeHeapDump: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "capture snapshot: %v\n", err)
		return 1
	}
	defer captured.Cleanup()

	snap, err := heapdump.Parse(captured.HeapDump, heapdump.Options{KeepObjectContents: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse heap dump: %v\n", err)
		return 1
	}
	mem := heaplabels.NewMemory(snap)
	want := map[string]string{key: value}
	candidates := heaplabels.FindOffsetCandidates(snap, mem, want, heaplabels.Options{})

	fmt.Printf("go version: %s\n", runtime.Version())
	fmt.Printf("heap dump build version: %s\n", snap.Params.BuildVersion)
	fmt.Printf("goarch: %s\n", snap.Params.GOARCH)
	fmt.Printf("ptr size: %d\n", snap.Params.PtrSize)
	fmt.Printf("goroutines: %d\n", len(snap.Goroutines))
	fmt.Printf("expected labels: %s=%s\n", key, value)
	fmt.Printf("candidate offsets: %d\n", len(candidates))
	for _, c := range candidates {
		fmt.Printf("  offset 0x%x matches=%d goroutines=%v\n", c.Offset, c.Matches, c.GoroutineIDs)
	}
	if len(candidates) != 1 {
		return 1
	}

	res := heaplabels.DecodeAll(snap, heaplabels.Options{
		GLabelsOffset:    candidates[0].Offset,
		HasGLabelsOffset: true,
	})
	fmt.Println()
	res.PrintSummary(os.Stdout)
	for _, gr := range res.Goroutines {
		if gr.Labels[key] == value {
			fmt.Printf("\ngoroutine %d labels:\n", gr.GID)
			for _, kv := range heaplabels.FormatLabels(gr.Labels) {
				fmt.Printf("  %s\n", kv)
			}
		}
	}

	time.Sleep(10 * time.Millisecond)
	return 0
}

func dynamicString(s string) string {
	return string([]byte(s))
}
