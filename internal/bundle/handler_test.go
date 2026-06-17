package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapdump"
)

func TestHandlerRejectsNonGET(t *testing.T) {
	h := Handler(HandlerOptions{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/debug/memusage/bundle", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s: Allow = %q, want GET", method, allow)
		}
		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["code"] != "invalid_method" {
			t.Errorf("%s: body = %v, err %v", method, body, err)
		}
	}
}

func TestHandlerRejectsBadGCParam(t *testing.T) {
	h := Handler(HandlerOptions{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/memusage/bundle?gc=maybe", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["code"] != "invalid_request" {
		t.Fatalf("body = %v, err %v", body, err)
	}
}

func TestHandlerBusyWhenSemaphoreHeld(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // simulate an in-flight request
	h := Handler(HandlerOptions{Semaphore: sem})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/memusage/bundle", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["code"] != "busy" {
		t.Fatalf("body = %v, err %v", body, err)
	}
}

func TestHandlerCaptureFailureBeforeStreamWritesJSON(t *testing.T) {
	h := Handler(HandlerOptions{
		CaptureFunc: func(context.Context, io.Writer, CaptureOptions) error {
			return errors.New("capture broke")
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/memusage/bundle", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != "capture_failed" {
		t.Fatalf("body = %v", body)
	}
}

func TestHandlerCaptureFailureAfterStreamAborts(t *testing.T) {
	h := Handler(HandlerOptions{
		CaptureFunc: func(_ context.Context, w io.Writer, _ CaptureOptions) error {
			_, _ = io.WriteString(w, "partial tar")
			return errors.New("capture broke")
		},
	})
	rec := httptest.NewRecorder()

	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("panic = %v, want http.ErrAbortHandler", got)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 headers already sent", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
			t.Fatalf("Content-Type = %q", ct)
		}
		if rec.Body.String() != "partial tar" {
			t.Fatalf("body = %q", rec.Body.String())
		}
	}()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/memusage/bundle", nil))
}

// TestHandlerServesParsableBundle captures a real bundle of the test
// process over the handler and verifies the tar opens and the embedded
// heap dump parses.
func TestHandlerServesParsableBundle(t *testing.T) {
	if testing.Short() {
		t.Skip("heap dump capture in -short mode")
	}
	h := Handler(HandlerOptions{Capture: CaptureOptions{GCBeforeHeapDump: true, Producer: "bubblepprof/test"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/memusage/bundle", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition")
	}

	b, err := Open(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()
	if b.Meta.Producer != "bubblepprof/test" {
		t.Errorf("Producer = %q", b.Meta.Producer)
	}

	f, err := os.Open(b.HeapDumpPath)
	if err != nil {
		t.Fatalf("open extracted dump: %v", err)
	}
	defer f.Close()
	snap, _, err := heapdump.ParseLazyContents(f, f, heapdump.Options{Strict: true})
	if err != nil {
		t.Fatalf("parse embedded heap dump: %v", err)
	}
	if snap.Params.BuildVersion == "" || len(snap.Goroutines) == 0 {
		t.Errorf("suspicious snapshot: version=%q goroutines=%d", snap.Params.BuildVersion, len(snap.Goroutines))
	}
}

// TestCaptureSelfRodataDegradation verifies the disabled path records
// the documented status and the bundle still opens.
func TestCaptureSelfRodataDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("heap dump capture in -short mode")
	}
	var buf bytes.Buffer
	if err := CaptureSelf(context.Background(), &buf, CaptureOptions{DisableRodata: true}); err != nil {
		t.Fatalf("CaptureSelf: %v", err)
	}
	b, err := Open(&buf)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()
	if b.Meta.Rodata.Status != RodataDisabled {
		t.Errorf("Rodata.Status = %q, want %q", b.Meta.Rodata.Status, RodataDisabled)
	}
	if b.Segments != nil {
		t.Error("Segments should be nil when rodata capture is disabled")
	}
}

// TestCaptureSelfIncludesRodata verifies a live capture embeds
// read-only segments that serve a string literal's bytes (Linux-gated
// inside via the process reader availability).
func TestCaptureSelfIncludesRodata(t *testing.T) {
	if testing.Short() {
		t.Skip("heap dump capture in -short mode")
	}
	var buf bytes.Buffer
	if err := CaptureSelf(context.Background(), &buf, CaptureOptions{}); err != nil {
		t.Fatalf("CaptureSelf: %v", err)
	}
	b, err := Open(&buf)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()
	if b.Meta.Rodata.Status == RodataUnavailable {
		t.Skipf("process memory reader unavailable on this platform: %s", b.Meta.Rodata.Reason)
	}
	if b.Segments == nil {
		t.Fatal("Segments is nil despite rodata status ok")
	}
	if b.Meta.Rodata.TotalBytes == 0 {
		t.Error("rodata snapshot is empty")
	}
}

func TestCaptureSelfCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CaptureSelf(ctx, io.Discard, CaptureOptions{}); err == nil {
		t.Fatal("CaptureSelf with cancelled context must error")
	}
}
