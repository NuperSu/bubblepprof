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
