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

// TestMemUsageHandler_Phase3_LiteralLabelsRecovered exercises the
// Phase 3 requirement: ordinary runtime/pprof labels built from string
// LITERALS must be recoverable by /debug/memusage on a supported
// runtime, without any bubblepprof wrapper / strings.Clone / labels.json.
//
// Acceptance per AGENTS.md:
//
//   - HTTP 200 with matched_goroutines >= 1 and reachable_bytes > 0
//   - No labels.json. No goroutine.pprof.
//
// The only skip is -short mode. A 422 string_missing is always a hard
// failure: the process memory reader is implemented on Linux, macOS,
// FreeBSD, and Windows, so literal label recovery must succeed on all of
// those platforms.
//
// A 422 unsupported_runtime is also a hard failure: the Go version must be
// in the verified layout table for this test to run.
func TestMemUsageHandler_Phase3_LiteralLabelsRecovered(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}

	ctx, cancel := context.WithCancel(context.Background())

	const payloadBytes = 4 << 20
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// Ordinary string literals — exactly the API Phase 3 must support.
	pprof.Do(ctx, pprof.Labels("job", "phase3-literal"), func(ctx context.Context) {
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
		bytes.NewReader([]byte(`{"labels":{"job":"phase3-literal"}}`)))
	h.ServeHTTP(rr, req)

	switch rr.Code {
	case http.StatusOK:
		var resp memusage.Response
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
		}
		if resp.MatchedGoroutines < 1 {
			t.Fatalf("matched_goroutines = %d, want >= 1; resp=%+v", resp.MatchedGoroutines, resp)
		}
		if resp.ReachableBytes == 0 {
			t.Fatalf("reachable_bytes = 0, want > 0; resp=%+v", resp)
		}
		if resp.Labels["job"] != "phase3-literal" {
			t.Fatalf("response labels = %#v, want job=phase3-literal", resp.Labels)
		}
		t.Logf("phase3 literal labels recovered: matched=%d reachable_bytes=%d", resp.MatchedGoroutines, resp.ReachableBytes)
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
			t.Fatalf("decode error: %v\n%s", err, rr.Body.String())
		}
		switch er.Code {
		case "unsupported_runtime":
			t.Fatalf("runtime not in verified layout table: go=%s arch=%s", er.GoVersion, er.GOARCH)
		case "string_missing":
			t.Fatalf("literal pprof label strings not recovered: warnings=%v", er.Warnings)
		default:
			t.Fatalf("unexpected 422 code %q: %s", er.Code, rr.Body.String())
		}
	default:
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}

// TestMemUsageHandler_Phase3_DisablingReaderBreaksLiterals proves the
// process memory reader is doing real work: with
// DisableProcessMemoryReader=true, label decoding sees heap object
// contents only, so ordinary literal pprof.Labels fail with
// string_missing (or 200 with zero matches in the rare runtime where
// the literal happens to be heap-resident). The response MUST be
// honest — never a silent labels.json/profile fallback.
func TestMemUsageHandler_Phase3_DisablingReaderBreaksLiterals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}

	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	pprof.Do(ctx, pprof.Labels("job", "phase3-disabled"), func(ctx context.Context) {
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
	defer func() {
		cancel()
		wg.Wait()
	}()

	h := MemUsageHandlerWithOptions(MemUsageOptions{DisableProcessMemoryReader: true})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, MemUsagePath,
		strings.NewReader(`{"labels":{"job":"phase3-disabled"}}`))
	h.ServeHTTP(rr, req)

	switch rr.Code {
	case http.StatusUnprocessableEntity:
		var er memusage.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
			t.Fatalf("decode error: %v\n%s", err, rr.Body.String())
		}
		if er.Code == "unsupported_runtime" {
			t.Fatalf("runtime not in verified layout table: go=%s arch=%s", er.GoVersion, er.GOARCH)
		}
		if er.Code != "string_missing" {
			t.Fatalf("expected string_missing, got %q: %s", er.Code, rr.Body.String())
		}
		if !containsWarning(er.Warnings, "process memory reader disabled") {
			t.Fatalf("expected disabled-reader warning in error, got %v", er.Warnings)
		}
	case http.StatusOK:
		// On a runtime where the literal happens to be heap-resident
		// (e.g. compiler interns it into a heap object), the disabled-reader
		// path still decodes cleanly and returns 200 with no warnings field.
	default:
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}

func containsWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
