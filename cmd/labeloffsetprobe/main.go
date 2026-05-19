// labeloffsetprobe is a development-only tool that captures a live heap
// dump from itself and probes for the byte offset of runtime.g.labels in
// the running Go runtime. Its output is a candidate runtimelayout.TableEntry
// that can be pasted into internal/runtimelayout/table.go to extend
// /debug/memusage support to a new Go version.
//
// Usage:
//
//	go run ./cmd/labeloffsetprobe
//
// Heap-allocated label strings are used for the probe so the bytes are
// guaranteed to appear in the heap dump object contents on every platform,
// independent of whether the in-process memory reader is available. The
// probe also opens the in-process reader (if supported on this OS) and
// prints which source it ended up using — useful when diagnosing FreeBSD
// configurations where procfs is unmounted and the ELF fallback applies.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/capture"
	"github.com/NuperSu/bubblepprof/internal/heapdump"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
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

	// Strict=true surfaces unknown tag IDs or other structural surprises
	// from a brand-new Go runtime as hard errors rather than silently
	// warning. That is exactly the case the probe targets, so the parser
	// must be loud.
	snap, err := heapdump.Parse(captured.HeapDump, heapdump.Options{
		KeepObjectContents: true,
		Strict:             true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse heap dump: %v\n", err)
		return 1
	}
	mem := heaplabels.NewMemory(snap)

	opts := heaplabels.Options{}
	procSource := "<not opened>"
	switch pr, err := addrspace.OpenSelfProcessReader(); {
	case err == nil:
		defer pr.Close()
		opts.ExtraStringMemory = pr
		procSource = pr.Source()
	default:
		fmt.Fprintf(os.Stderr, "warning: process memory reader unavailable: %v\n", err)
		procSource = fmt.Sprintf("<unavailable: %v>", err)
	}

	inHeap := 0
	for _, g := range snap.Goroutines {
		if _, ok := mem.Read(g.Addr, 8); ok {
			inHeap++
		}
	}

	want := map[string]string{key: value}
	candidates := heaplabels.FindOffsetCandidates(snap, mem, want, opts)

	input := heaplabels.LookupInputFromSnapshot(snap)
	_, alreadyKnown := runtimelayout.Lookup(input)

	fmt.Printf("go version (process):        %s\n", runtime.Version())
	fmt.Printf("go version (dump build):     %s\n", input.GoVersion)
	fmt.Printf("goos / goarch (process):     %s / %s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("goarch (dump):               %s\n", input.GOARCH)
	fmt.Printf("ptr size:                    %d\n", input.PtrSize)
	fmt.Printf("big endian:                  %t\n", input.BigEndian)
	fmt.Printf("process reader source:       %s\n", procSource)
	fmt.Printf("goroutines:                  %d\n", len(snap.Goroutines))
	fmt.Printf("goroutines with g in heap:   %d\n", inHeap)
	fmt.Printf("verified-table entry exists: %t\n", alreadyKnown)
	fmt.Printf("expected labels:             %s=%s\n", key, value)
	fmt.Printf("candidate offsets:           %d\n", len(candidates))
	for _, c := range candidates {
		fmt.Printf("  offset 0x%x matches=%d goroutines=%v\n", c.Offset, c.Matches, c.GoroutineIDs)
	}
	if len(candidates) != 1 {
		fmt.Fprintf(os.Stderr, "\nprobe inconclusive: want exactly 1 candidate, got %d\n", len(candidates))
		return 1
	}

	manual, err := runtimelayout.Manual(input, candidates[0].Offset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtimelayout.Manual: %v\n", err)
		return 1
	}
	fmt.Println()
	if alreadyKnown {
		fmt.Println("note: the verified-layout table already covers this runtime;")
		fmt.Println("      the entry below is informational and should match the existing one.")
	} else {
		fmt.Println("paste the following into internal/runtimelayout/table.go:")
	}
	fmt.Println()
	fmt.Println("suggested runtimelayout.TableEntry:")
	fmt.Printf("  VersionPrefix: %q\n", input.GoVersion)
	fmt.Printf("  GOARCH:        %q\n", input.GOARCH)
	fmt.Printf("  PtrSize:       %d\n", input.PtrSize)
	fmt.Printf("  BigEndian:     %t\n", input.BigEndian)
	fmt.Printf("  GLabelsOffset: 0x%x\n", manual.GLabelsOffset)

	res := heaplabels.DecodeAll(snap, manual, opts)
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

// dynamicString forces a heap allocation so the underlying bytes appear in
// the heap dump's object contents on every platform — independent of
// whether the in-process address-space reader is available. The probe must
// work on FreeBSD-PIE-without-procfs, where the literal-string path would
// not be readable.
func dynamicString(s string) string {
	return string([]byte(s))
}
