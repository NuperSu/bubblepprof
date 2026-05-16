package memusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHandler_RejectsNonPost(t *testing.T) {
	h := Handler(stubCompute(nil, nil), HandlerOptions{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rr.Code)
			}
			if got := rr.Header().Get("Allow"); got != "POST" {
				t.Fatalf("Allow = %q, want POST", got)
			}
			var body ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Code != "invalid_method" {
				t.Fatalf("code = %q", body.Code)
			}
		})
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	h := Handler(stubCompute(nil, nil), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader("{not json"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "invalid_request" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestHandler_ValidationError(t *testing.T) {
	h := Handler(stubCompute(nil, nil), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "empty_labels" {
		t.Fatalf("code = %q, want empty_labels", body.Code)
	}
}

func TestHandler_HappyPath(t *testing.T) {
	want := &Response{
		Labels:            map[string]string{"job": "42"},
		MatchedGoroutines: 2,
		ReachableObjects:  3,
		ReachableBytes:    60,
		Attribution:       AttributionHeapNative,
	}
	h := Handler(stubCompute(want, nil), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"job":"42"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q", got)
	}
	var got Response
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.MatchedGoroutines != 2 || got.ReachableBytes != 60 || got.Attribution != AttributionHeapNative {
		t.Fatalf("body = %+v", got)
	}
}

func TestHandler_UnsupportedRuntime(t *testing.T) {
	h := Handler(stubCompute(nil, &UnsupportedRuntimeError{GoVersion: "go1.27.0", GOARCH: "amd64"}), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"job":"42"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "unsupported_runtime" || body.GoVersion != "go1.27.0" || body.GOARCH != "amd64" {
		t.Fatalf("error body = %+v", body)
	}
	if body.Attribution != AttributionUnsupportedRuntime {
		t.Fatalf("attribution = %q, want %q", body.Attribution, AttributionUnsupportedRuntime)
	}
}

func TestHandler_StringMissing(t *testing.T) {
	h := Handler(stubCompute(nil, &StringMissingError{GoVersion: "go1.26.3", Warnings: []string{"missing strings"}}), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"job":"42"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "string_missing" {
		t.Fatalf("code = %q", body.Code)
	}
	if body.Attribution != AttributionHeapNativeIncomplete {
		t.Fatalf("attribution = %q", body.Attribution)
	}
	if len(body.Warnings) != 1 {
		t.Fatalf("warnings = %v", body.Warnings)
	}
}

func TestHandler_Busy(t *testing.T) {
	// Block the first compute call until we explicitly release it; then
	// fire a second concurrent request and assert it gets 429.
	release := make(chan struct{})
	started := make(chan struct{})
	compute := func(ctx context.Context, req Request) (*Response, error) {
		close(started)
		<-release
		return &Response{Attribution: AttributionHeapNative, Labels: req.Labels}, nil
	}
	h := Handler(compute, HandlerOptions{})

	var wg sync.WaitGroup
	wg.Add(1)
	var firstStatus int
	go func() {
		defer wg.Done()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
		h.ServeHTTP(rr, req)
		firstStatus = rr.Code
	}()

	<-started // first request has the semaphore

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent request status = %d, want 429", rr.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "busy" {
		t.Fatalf("code = %q", body.Code)
	}

	close(release)
	wg.Wait()
	if firstStatus != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", firstStatus)
	}
}

func TestHandler_BodyTooLarge(t *testing.T) {
	huge := strings.Repeat("a", 4096)
	body := `{"labels":{"job":"` + huge + `"}}`
	h := Handler(stubCompute(nil, nil), HandlerOptions{MaxRequestBodyBytes: 128})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(body))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 from MaxBytesReader truncation", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "invalid_request" {
		t.Fatalf("code = %q", er.Code)
	}
}

func TestHandler_RejectsTrailingJSON(t *testing.T) {
	h := Handler(stubCompute(nil, nil), HandlerOptions{})
	cases := []struct {
		name string
		body string
	}{
		{"trailing object", `{"labels":{"job":"42"}} {"extra":true}`},
		{"trailing array", `{"labels":{"job":"42"}} []`},
		{"trailing literal", `{"labels":{"job":"42"}} null`},
		{"trailing garbage", `{"labels":{"job":"42"}} not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(tc.body))
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%q)", rr.Code, tc.body)
			}
			var er ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if er.Code != "invalid_request" {
				t.Fatalf("code = %q, want invalid_request", er.Code)
			}
		})
	}
}

func TestHandler_RejectsUnknownFields(t *testing.T) {
	h := Handler(stubCompute(nil, nil), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage",
		strings.NewReader(`{"labels":{"job":"42"},"extra":true}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "invalid_request" {
		t.Fatalf("code = %q", er.Code)
	}
}

func TestHandler_ValidationFromCompute(t *testing.T) {
	// Simulate a compute function that returns a *ValidationError. The
	// handler must translate it to 400 even though no handler-level
	// validation triggered.
	h := Handler(stubCompute(nil, NewValidationError("custom_code", "boom")), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage",
		strings.NewReader(`{"labels":{"job":"42"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "custom_code" {
		t.Fatalf("code = %q", er.Code)
	}
}

func TestHandler_InternalError(t *testing.T) {
	h := Handler(stubCompute(nil, errors.New("boom")), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"job":"42"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "internal_error" {
		t.Fatalf("code = %q", er.Code)
	}
}

func TestHandler_ZeroMatchReturns200(t *testing.T) {
	// A valid request that matches no goroutines must return 200 with zero
	// counts, not an error. "no match" is not an error condition.
	want := &Response{
		Labels:            map[string]string{"job": "missing"},
		MatchedGoroutines: 0,
		ReachableObjects:  0,
		ReachableBytes:    0,
		Attribution:       AttributionHeapNative,
		Warnings:          []string{},
	}
	h := Handler(stubCompute(want, nil), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"job":"missing"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for zero-match", rr.Code)
	}
	var got Response
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.MatchedGoroutines != 0 || got.ReachableObjects != 0 || got.ReachableBytes != 0 {
		t.Fatalf("expected zero counts, got %+v", got)
	}
	if got.Warnings == nil {
		t.Fatal("warnings must be [] not null in JSON response")
	}
}

func TestHandler_WarningsAlwaysPresent(t *testing.T) {
	// Both success and error responses must emit "warnings": [] not null.
	h := Handler(stubCompute(&Response{
		Labels:      map[string]string{"a": "b"},
		Attribution: AttributionHeapNative,
	}, nil), HandlerOptions{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	raw := rr.Body.Bytes()
	if !strings.Contains(string(raw), `"warnings":`) {
		t.Fatalf("response body missing warnings field: %s", raw)
	}

	// Error path: validation failure.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{}}`))
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"warnings":`) {
		t.Fatalf("error response body missing warnings field: %s", rr.Body.String())
	}
}

func TestHandler_CaptureFailed(t *testing.T) {
	h := Handler(stubCompute(nil, &CaptureFailedError{Cause: errors.New("disk full")}), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "capture_failed" {
		t.Fatalf("code = %q, want capture_failed", er.Code)
	}
}

func TestHandler_ParseFailed(t *testing.T) {
	h := Handler(stubCompute(nil, &ParseFailedError{Cause: errors.New("truncated")}), HandlerOptions{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/memusage", strings.NewReader(`{"labels":{"a":"b"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if er.Code != "parse_failed" {
		t.Fatalf("code = %q, want parse_failed", er.Code)
	}
}

func stubCompute(resp *Response, err error) ComputeFunc {
	return func(ctx context.Context, req Request) (*Response, error) {
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return &Response{Labels: req.Labels, Attribution: AttributionHeapNative}, nil
		}
		return resp, nil
	}
}
