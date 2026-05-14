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
			if body.Code != "method_not_allowed" {
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
	if er.Code != "internal" {
		t.Fatalf("code = %q", er.Code)
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
