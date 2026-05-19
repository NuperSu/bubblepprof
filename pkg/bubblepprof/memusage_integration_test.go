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

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// TestMemUsageHandler_RuntimePprofLabels exercises /debug/memusage against
// the live runtime. The probed goroutine is labeled with the standard
// runtime/pprof API — no bubblepprof wrapper, no labels.json, no
// goroutine.pprof correlation.
//
// The endpoint must return coherent JSON; silent fallback to labels.json
// or goroutine.pprof is a regression. 422 unsupported_runtime is a hard
// failure — the Go version must be in the verified layout table.
func TestMemUsageHandler_RuntimePprofLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}

	// strings.Clone forces heap allocation so the label bytes appear in
	// heap-dump object contents. This test exercises heap-object decoding;
	// literal-string recovery via the process reader is covered by
	// TestMemUsageHandler_RuntimePprofLiteralLabels.
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
		t.Logf("memusage 200: matched=%d reachable_objects=%d reachable_bytes=%d",
			resp.MatchedGoroutines, resp.ReachableObjects, resp.ReachableBytes)
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
		if er.Code == "unsupported_runtime" {
			t.Fatalf("runtime not in verified layout table: go=%s arch=%s", er.GoVersion, er.GOARCH)
		}
		if er.Code != "string_missing" {
			t.Fatalf("unexpected error code %q for 422; want string_missing", er.Code)
		}
		t.Logf("memusage returned honest diagnostics: code=%q go_version=%q goarch=%q",
			er.Code, er.GoVersion, er.GOARCH)
	default:
		t.Fatalf("unexpected status %d; body=%s", rr.Code, rr.Body.String())
	}
}

// TestMemUsageHandler_RuntimePprofLiteralLabels exercises the Phase 3
// path that recovers literal pprof.Labels strings from the in-process
// address space. On supported runtimes (Linux + Go 1.26.x amd64), the
// endpoint should return 200; the stricter end-to-end assertions live in
// TestMemUsageHandler_Phase3_LiteralLabelsRecovered.
//
// A 422 string_missing is a hard failure on Linux, macOS, Windows, and on
// FreeBSD when procfs is mounted at /proc or the binary is non-PIE — those
// are the configurations where the in-process reader is implemented.
// On FreeBSD-PIE without procfs (and on platforms where the reader is not
// implemented), failing here is the correct signal that literal string
// recovery is not yet supported. A 422 unsupported_runtime is also a hard
// failure. The response must NEVER silently fall back to labels.json or
// goroutine.pprof.
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
		// A 200 must actually match the labeled goroutine; otherwise the
		// endpoint silently returned an empty result and the test would
		// have falsely "succeeded".
		if resp.MatchedGoroutines < 1 {
			t.Fatalf("matched_goroutines = %d, want >= 1; resp = %+v", resp.MatchedGoroutines, resp)
		}
		if resp.ReachableBytes == 0 {
			t.Fatalf("reachable_bytes = 0, want > 0; resp = %+v", resp)
		}
		if resp.Labels["job"] != "literal-test" {
			t.Fatalf("response labels = %#v, want job=literal-test", resp.Labels)
		}
		t.Logf("literal labels recoverable on this runtime: matched=%d reachable_bytes=%d",
			resp.MatchedGoroutines, resp.ReachableBytes)
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
			t.Fatalf("decode error response: %v\n%s", err, rr.Body.String())
		}
		if er.Code == "unsupported_runtime" {
			t.Fatalf("runtime not in verified layout table: go=%s arch=%s", er.GoVersion, er.GOARCH)
		}
		t.Fatalf("literal pprof label strings not recovered: code=%q warnings=%v", er.Code, er.Warnings)
	default:
		t.Fatalf("unexpected status %d; body=%s", rr.Code, rr.Body.String())
	}
}
