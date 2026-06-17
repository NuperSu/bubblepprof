package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// reachabilityPayload is the retained heap allocation in each test worker.
// Pad is large so reachable_bytes assertions stay comfortably above noise.
type reachabilityPayload struct {
	Pad []byte
}

type reachabilityMapCarrier struct {
	M map[int]*reachabilityPayload
}

// newReachabilityMapCarrier constructs the map fixture through a noinline
// helper so the carrier and runtime map header are heap objects. The test is
// about heap traversal through map storage, not compiler placement of a tiny
// local variable at a blocking receive.
//
//go:noinline
func newReachabilityMapCarrier(capHint int) *reachabilityMapCarrier {
	return &reachabilityMapCarrier{
		M: make(map[int]*reachabilityPayload, capHint),
	}
}

// reachabilityCarrier holds an any field on the heap. Using a heap-resident
// struct ensures the interface data pointer is part of the object's GC
// pointer bitmap (FieldKindPtr), not a stack eface record.
type reachabilityCarrier struct {
	Iface any
}

const (
	reachabilityPayloadSize = 2 << 20 // 2 MiB per worker
	reachabilityMinBytes    = 1 << 20 // conservative 1 MiB assertion floor
)

// TestMemUsageHandler_ReachabilityThroughRuntimeDataStructures validates
// that /debug/memusage traverses heap objects reachable from pprof-labeled
// goroutines through real Go runtime data structures:
//
//   - Slice backing arrays with len < cap (hidden elements must be counted).
//   - Map values (values pointed to from runtime map storage).
//   - Large maps (values across multi-object map backing storage).
//   - Buffered channels (objects queued in channel buffers).
//   - Interface values (data pointer inside a heap-resident eface struct).
//   - Finalizer records (finalizable objects also appear under global roots).
//
// Each subtest starts a goroutine labeled with case=<label> that holds
// ≥2 MiB of memory, then posts to /debug/memusage and asserts
// reachable_bytes ≥ 1 MiB. A 422 unsupported_runtime fails the subtest;
// 422 string_missing skips it (platform limitation, not a reachability bug).
//
// Workers use plain runtime/pprof label literals — no bubblepprof wrappers,
// no strings.Clone, no dynamicString.
func TestMemUsageHandler_ReachabilityThroughRuntimeDataStructures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump reachability test in short mode")
	}

	mux := http.NewServeMux()
	mux.Handle(MemUsagePath, MemUsageHandler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	stop := make(chan struct{})
	defer close(stop)

	// Slice backing array: hidden is at index 2 of the backing array but the
	// slice is shrunk to len=2. The GC pointer bitmap covers the full cap=100
	// backing array, so index 2 must appear in the goroutine's stack roots.
	//
	// runtime.KeepAlive is placed AFTER <-stop (not in a defer) so that xs is
	// in the GC liveness bitmap at the channel-receive instruction. Putting it
	// only in a defer would cause the compiler to treat xs as dead at <-stop,
	// allowing GC to collect the payload before the heap dump is written.
	startReachabilityWorker(t, "slice-backing", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		hidden := &reachabilityPayload{Pad: make([]byte, reachabilityPayloadSize)}
		xs := make([]*reachabilityPayload, 3, 100)
		xs[2] = hidden
		xs = xs[:2] // len=2, cap=100; hidden is beyond len but in the backing array
		close(ready)
		<-stop
		runtime.KeepAlive(xs) // must be here so xs is live at <-stop above
	})

	// Map values reachable through runtime map storage pointer slots.
	// Keep the map inside a heap-resident carrier. This makes the intended root
	// path explicit:
	//
	//   goroutine stack root -> carrier -> runtime map -> map storage -> payloads
	//
	// and avoids depending on the compiler's representation of a bare local map
	// variable around the blocking receive.
	startReachabilityWorker(t, "map-values", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		carrier := newReachabilityMapCarrier(128)
		for i := 0; i < 128; i++ {
			carrier.M[i] = &reachabilityPayload{
				Pad: make([]byte, 16<<10), // 16 KiB each, total ~2 MiB
			}
		}
		close(ready)
		<-stop
		runtime.KeepAlive(carrier)
	})

	// Large map storage: 10,000 entries forces the runtime to allocate
	// multi-object backing storage (overflow buckets on older runtimes,
	// table/group storage on newer runtimes). The full ~10 MiB must be
	// reachable through the map.
	startReachabilityWorker(t, "map-overflow", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		carrier := newReachabilityMapCarrier(10000)
		for i := 0; i < 10000; i++ {
			carrier.M[i] = &reachabilityPayload{Pad: make([]byte, 1<<10)} // 1 KiB each
		}
		close(ready)
		<-stop
		runtime.KeepAlive(carrier)
	})

	// Buffered channel: payload is queued in the channel buffer (hchan
	// internal circular buffer), not referenced by any explicit variable.
	startReachabilityWorker(t, "channel-buffer", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		ch := make(chan *reachabilityPayload, 10)
		ch <- &reachabilityPayload{Pad: make([]byte, reachabilityPayloadSize)}
		close(ready)
		<-stop
		runtime.KeepAlive(ch)
	})

	// Heap-allocated defer record: a defer inside a loop body cannot use
	// deferprocStack, so the runtime heap-allocates the _defer record and
	// links it from gp._defer. Once the loop-scoped payload local is dead,
	// the deferred closure (and the payload it captures) is reachable only
	// through the goroutine record's defer pointer — the graph builder must
	// root gp._defer per goroutine for this memory to count.
	startReachabilityWorker(t, "heap-defer", stop, deferHoldPayload)

	// Interface value: payload held as any inside a heap-resident struct.
	// The eface data pointer is in the heap object's GC bitmap (FieldKindPtr),
	// so it is decoded into a graph edge by the parser.
	// carrier.Iface = payload is a write through carrier (same pattern as
	// xs[2]=hidden for slices), which keeps carrier in the liveness bitmap.
	startReachabilityWorker(t, "interface-value", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		carrier := &reachabilityCarrier{}
		carrier.Iface = &reachabilityPayload{Pad: make([]byte, reachabilityPayloadSize)}
		close(ready)
		<-stop
		runtime.KeepAlive(carrier)
	})

	// Finalizer root: the payload is retained by the labeled goroutine and
	// registered for finalization. Its reachable bytes must therefore also
	// appear in the response's global-root overlap.
	startReachabilityWorker(t, "finalizer-root", stop, func(ready chan<- struct{}, stop <-chan struct{}) {
		payload := &reachabilityPayload{Pad: make([]byte, reachabilityPayloadSize)}
		runtime.SetFinalizer(payload, func(*reachabilityPayload) {})
		close(ready)
		<-stop
		runtime.KeepAlive(payload)
	})

	cases := []struct {
		label          string
		minBytes       uint64
		minGlobalBytes uint64
	}{
		{label: "slice-backing", minBytes: reachabilityMinBytes},
		{label: "map-values", minBytes: reachabilityMinBytes},
		{label: "map-overflow", minBytes: 4 << 20}, // ~10 MiB total across overflow buckets; conservative floor
		{label: "channel-buffer", minBytes: reachabilityMinBytes},
		{label: "heap-defer", minBytes: reachabilityMinBytes},
		{label: "interface-value", minBytes: reachabilityMinBytes},
		{label: "finalizer-root", minBytes: reachabilityMinBytes, minGlobalBytes: reachabilityMinBytes},
	}
	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			assertReachableAtLeast(t, srv.URL, c.label, c.minBytes, c.minGlobalBytes)
		})
	}
}

// deferLoopTrips is read through a package variable so the compiler cannot
// prove deferHoldPayload's loop is single-trip. A provably single-trip loop
// would let the defer be open-coded or stack-allocated, defeating the test.
var deferLoopTrips = 1

// deferHoldPayload parks with a pending heap-allocated _defer whose closure
// captures a ≥2 MiB payload. The defer sits in a loop so the compiler must
// use deferproc with a heap _defer record (deferprocStack and open-coded
// defers are unavailable for loop defers). At the park point both payload
// and the closure are loop-scoped and dead in the stack liveness bitmap, so
// gp._defer is the only path keeping the payload reachable.
//
// The park MUST be time.Sleep, not <-stop: a channel receive leaves a sudog
// pointer in the runtime.chanrecv frame, and sudog.g reaches the g object,
// whose own pointer fields include _defer — that accidental stack→sudog→g
// path makes the payload frame-reachable and the test would pass even
// without per-goroutine defer roots. Sleeping parks without a sudog on the
// stack, so only the goroutine record's defer pointer can attribute the
// payload. The goroutine intentionally outlives the test (sleeps ~1h, no
// stop hookup); it holds 2 MiB until the test binary exits, which is
// harmless and keeps the park state deterministic.
//
//go:noinline
func deferHoldPayload(ready chan<- struct{}, _ <-chan struct{}) {
	for i := 0; i < deferLoopTrips; i++ {
		payload := &reachabilityPayload{Pad: make([]byte, reachabilityPayloadSize)}
		defer func() { runtime.KeepAlive(payload) }()
	}
	close(ready)
	time.Sleep(time.Hour)
}

// startReachabilityWorker launches a goroutine labeled with case=label.
// It calls f with a ready channel and the stop channel. f must close ready
// after its data structures are fully set up, then block on <-stop.
func startReachabilityWorker(t *testing.T, label string, stop <-chan struct{}, f func(ready chan<- struct{}, stop <-chan struct{})) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		ctx := context.Background()
		pprof.Do(ctx, pprof.Labels("case", label), func(ctx context.Context) {
			pprof.SetGoroutineLabels(ctx)
			f(ready, stop)
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker %q did not signal ready within 5s", label)
	}
}

// processMemoryReaderSupported reports whether the current GOOS has a working
// process memory reader. On these platforms string_missing is a product
// regression, not a platform limitation.
func processMemoryReaderSupported() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "windows":
		return true
	}
	return false
}

// assertReachableAtLeast posts {"labels":{"case":label}} to /debug/memusage
// and asserts reachable_bytes >= minBytes.
//
// A 422 unsupported_runtime is always a hard failure. A 422 string_missing
// is a hard failure on platforms with a verified process memory reader
// (Linux, macOS, Windows, and FreeBSD with procfs mounted or a non-PIE
// binary); on other platforms it signals a skip.
func assertReachableAtLeast(t *testing.T, baseURL, label string, minBytes, minGlobalBytes uint64) {
	t.Helper()
	body := bytes.NewReader([]byte(`{"labels":{"case":"` + label + `"}}`))
	resp, err := http.Post(baseURL+MemUsagePath, "application/json", body)
	if err != nil {
		t.Fatalf("POST /debug/memusage: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var mr memusage.Response
		if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
			t.Fatalf("decode 200 response: %v", err)
		}
		t.Logf("label=%q: matched=%d objects=%d bytes=%d",
			label, mr.MatchedGoroutines, mr.ReachableObjects, mr.ReachableBytes)
		if mr.MatchedGoroutines < 1 {
			t.Errorf("label=%q: matched_goroutines=%d, want >=1 (label not found in heap dump)",
				label, mr.MatchedGoroutines)
			return
		}
		if mr.ReachableBytes < minBytes {
			t.Errorf("label=%q: reachable_bytes=%d, want >=%d — "+
				"check heap pointer traversal for this data structure type",
				label, mr.ReachableBytes, minBytes)
		}
		if mr.GlobalOverlapBytes < minGlobalBytes {
			t.Errorf("label=%q: global_overlap_bytes=%d, want >=%d — "+
				"check finalizer/global root traversal",
				label, mr.GlobalOverlapBytes, minGlobalBytes)
		}
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
			t.Fatalf("decode 422 response: %v", err)
		}
		if er.Code == "unsupported_runtime" {
			t.Fatalf("label=%q: runtime not in verified layout table: go=%s arch=%s",
				label, er.GoVersion, er.GOARCH)
		}
		if processMemoryReaderSupported() {
			t.Fatalf("label=%q: label recovery returned %q on %s — process memory reader is implemented here, this is a regression: warnings=%v",
				label, er.Code, runtime.GOOS, er.Warnings)
		}
		t.Skipf("label=%q: label recovery returned %q on %s — process memory reader not supported on this platform",
			label, er.Code, runtime.GOOS)
	default:
		t.Fatalf("label=%q: unexpected status %d", label, resp.StatusCode)
	}
}
