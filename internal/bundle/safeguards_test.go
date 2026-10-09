package bundle

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBundleCooldown(t *testing.T) {
	called := 0
	h := Handler(HandlerOptions{Cooldown: time.Hour, CaptureFunc: func(context.Context, io.Writer, CaptureOptions) error { called++; return nil }})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if i == 1 && (w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "cooldown")) {
			t.Fatal(w)
		}
	}
	if called != 1 {
		t.Fatal(called)
	}
}
func TestCaptureArtifactAndHeapLimits(t *testing.T) {
	for _, limit := range []int64{-1, 1} {
		dir := t.TempDir()
		var out bytes.Buffer
		err := CaptureSelf(context.Background(), &out, CaptureOptions{ScratchDir: dir, MaxArtifactBytes: limit, DisableRodata: true})
		if err == nil {
			t.Fatal("artifact limit ignored")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("scratch leak", entries)
		}
	}
	var out bytes.Buffer
	dir := t.TempDir()
	if err := CaptureSelf(context.Background(), &out, CaptureOptions{ScratchDir: dir, MaxHeapDumpBytes: 1}); err == nil {
		t.Fatal("heap limit ignored")
	}
	if out.Len() != 0 {
		t.Fatal("heap limit emitted bytes")
	}
}
