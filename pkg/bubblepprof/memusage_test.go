package bubblepprof

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bubblepprof/internal/memusage"
)

func TestMemUsageHandler_RejectsNonPost(t *testing.T) {
	h := MemUsageHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, MemUsagePath, nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestMemUsageHandler_ValidationError(t *testing.T) {
	h := MemUsageHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, MemUsagePath, strings.NewReader(`{"labels":{}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var body memusage.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "empty_labels" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestMemUsageOptions_DefaultsKeepGCOn(t *testing.T) {
	// Phase-1 review fix: the zero value of MemUsageOptions must match
	// MemUsageHandler()'s defaults so a caller flipping an unrelated
	// flag never accidentally turns the pre-dump GC off.
	cases := []struct {
		name    string
		opts    MemUsageOptions
		wantGC  bool
		wantSys bool
	}{
		{name: "zero", opts: MemUsageOptions{}, wantGC: true, wantSys: false},
		{name: "disable gc", opts: MemUsageOptions{DisableGCBeforeHeapDump: true}, wantGC: false, wantSys: false},
		{name: "include system only", opts: MemUsageOptions{IncludeSystemGoroutines: true}, wantGC: true, wantSys: true},
		{name: "both", opts: MemUsageOptions{DisableGCBeforeHeapDump: true, IncludeSystemGoroutines: true}, wantGC: false, wantSys: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.toInternal()
			if got.GCBeforeHeapDump != tc.wantGC {
				t.Fatalf("GCBeforeHeapDump = %t, want %t", got.GCBeforeHeapDump, tc.wantGC)
			}
			if got.IncludeSystemGoroutines != tc.wantSys {
				t.Fatalf("IncludeSystemGoroutines = %t, want %t", got.IncludeSystemGoroutines, tc.wantSys)
			}
		})
	}
}

func TestMemUsageOptions_DisableProcessMemoryReaderFlows(t *testing.T) {
	if got := (MemUsageOptions{}).toInternal(); got.DisableProcessMemoryReader {
		t.Fatal("zero value must keep process memory reader enabled")
	}
	got := MemUsageOptions{DisableProcessMemoryReader: true}.toInternal()
	if !got.DisableProcessMemoryReader {
		t.Fatal("DisableProcessMemoryReader must propagate to internal Options")
	}
}

func TestRegisterMemUsage(t *testing.T) {
	mux := http.NewServeMux()
	RegisterMemUsage(mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, MemUsagePath, strings.NewReader(`{"labels":{}}`))
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (handler reachable on %s)", rr.Code, MemUsagePath)
	}
}
