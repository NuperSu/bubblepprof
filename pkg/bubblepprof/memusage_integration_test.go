package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"bubblepprof/internal/memusage"
)

// TestMemUsageHandler_RuntimePprofLabels exercises /debug/memusage against
// the live runtime. The probed goroutine is labeled with the standard
// runtime/pprof API — no bubblepprof wrapper, no labels.json, no
// goroutine.pprof correlation.
//
// Phase 1's heap-native decoder is verified only for a narrow runtime
// family (Go 1.26.x / amd64 / ptr size 8). On other runtimes the
// endpoint must return honest diagnostics. This test accepts:
//
//   - 200 with matched_goroutines >= 1 and reachable_bytes > 0
//   - 422 unsupported_runtime
//   - 422 string_missing
//
// In every case the response must be coherent JSON; silent fallback to
// labels.json or goroutine.pprof is a regression.
func TestMemUsageHandler_RuntimePprofLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}

	// Heap-native label recovery has to read the key/value strings out
	// of heap-resident bytes. Cloning the literals onto the heap is the
	// Phase 1 workaround until the rodata/executable reader lands.
	bubbleKey := strings.Clone("job")
	bubbleValue := strings.Clone("alpha")

	ctx, cancel := context.WithCancel(context.Background())

	const payloadBytes = 8 << 20 // 8 MiB
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	pprof.Do(ctx, pprof.Labels(bubbleKey, bubbleValue), func(ctx context.Context) {
		go func() {
			defer wg.Done()
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, payloadBytes)
			for i := 0; i < len(data); i += 4096 {
				data[i] = byte(i)
			}
			close(started)
			<-ctx.Done()
			runtime.KeepAlive(data)
		}()
	})
	<-started

	// Tear down in reverse construction order. Cancel must happen
	// before wg.Wait so the labeled goroutine actually returns;
	// otherwise we deadlock on the WaitGroup.
	defer func() {
		cancel()
		wg.Wait()
	}()

	h := MemUsageHandler()
	body := bytes.NewReader([]byte(`{"labels":{"` + bubbleKey + `":"` + bubbleValue + `"}}`))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, MemUsagePath, body)
	h.ServeHTTP(rr, req)

	switch rr.Code {
	case http.StatusOK:
		var resp memusage.Response
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
		}
		t.Logf("memusage 200: attribution=%q matched=%d reachable_objects=%d reachable_bytes=%d go=%s arch=%s",
			resp.Attribution, resp.MatchedGoroutines, resp.ReachableObjects, resp.ReachableBytes,
			resp.GoVersion, resp.GOARCH)
		if resp.Attribution != memusage.AttributionHeapNative &&
			resp.Attribution != memusage.AttributionHeapNativeIncomplete {
			t.Fatalf("unexpected attribution %q (must be heap-native flavor)", resp.Attribution)
		}
		if resp.MatchedGoroutines < 1 {
			t.Fatalf("matched_goroutines = %d, want >= 1; resp = %+v", resp.MatchedGoroutines, resp)
		}
		if resp.ReachableBytes == 0 {
			t.Fatalf("reachable_bytes = 0, want > 0; resp = %+v", resp)
		}
		if resp.Labels[bubbleKey] != bubbleValue {
			t.Fatalf("response labels missing requested selector: %v", resp.Labels)
		}
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
			t.Fatalf("decode error response: %v\n%s", err, rr.Body.String())
		}
		if er.Code != "unsupported_runtime" && er.Code != "string_missing" {
			t.Fatalf("unexpected error code %q for 422; want unsupported_runtime or string_missing", er.Code)
		}
		t.Logf("memusage returned honest diagnostics: code=%q go_version=%q goarch=%q",
			er.Code, er.GoVersion, er.GOARCH)
	default:
		t.Fatalf("unexpected status %d; body=%s", rr.Code, rr.Body.String())
	}
}

// TestMemUsageHandler_RuntimePprofLiteralLabels documents the known
// Phase 1 limitation: when the profiled program passes ordinary string
// LITERALS to runtime/pprof.Labels (the common case), the bytes that
// back those literals live in executable rodata, not heap objects, and
// WriteHeapDump does not capture them. Phase 3 plans to add an
// executable/process-memory reader to recover them.
//
// In the meantime, the endpoint MUST be honest: it may return either a
// 200 if the runtime happens to allocate label bytes on the heap, or a
// 422 string_missing diagnostic. It must NEVER silently fall back to
// labels.json or goroutine.pprof.
func TestMemUsageHandler_RuntimePprofLiteralLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}

	ctx, cancel := context.WithCancel(context.Background())

	const payloadBytes = 4 << 20 // 4 MiB
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// Plain string literals — exactly the pprof-compatible API.
	pprof.Do(ctx, pprof.Labels("job", "literal-test"), func(ctx context.Context) {
		go func() {
			defer wg.Done()
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, payloadBytes)
			for i := 0; i < len(data); i += 4096 {
				data[i] = byte(i)
			}
			close(started)
			<-ctx.Done()
			runtime.KeepAlive(data)
		}()
	})
	<-started
	defer func() {
		cancel()
		wg.Wait()
	}()

	h := MemUsageHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, MemUsagePath,
		strings.NewReader(`{"labels":{"job":"literal-test"}}`))
	h.ServeHTTP(rr, req)

	switch rr.Code {
	case http.StatusOK:
		var resp memusage.Response
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
		}
		if resp.Attribution != memusage.AttributionHeapNative &&
			resp.Attribution != memusage.AttributionHeapNativeIncomplete {
			t.Fatalf("unexpected attribution %q", resp.Attribution)
		}
		t.Logf("literal labels recoverable on this runtime: attribution=%q matched=%d",
			resp.Attribution, resp.MatchedGoroutines)
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
			t.Fatalf("decode error response: %v\n%s", err, rr.Body.String())
		}
		if er.Code != "unsupported_runtime" && er.Code != "string_missing" {
			t.Fatalf("422 code = %q; want unsupported_runtime or string_missing", er.Code)
		}
		t.Logf("literal labels not recoverable yet (Phase 3 target): code=%q", er.Code)
	default:
		t.Fatalf("unexpected status %d; body=%s", rr.Code, rr.Body.String())
	}
}
