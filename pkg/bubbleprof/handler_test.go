package bubbleprof

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"delve_first_project/internal/capture"
	"delve_first_project/internal/snapshot"
)

func TestHandlerReturnsValidTar(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)

	handler(testCaptureOptions(nil, nil)).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != `attachment; filename="bubbleprof-snapshot.tar"` {
		t.Fatalf("content-disposition = %q", got)
	}

	bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if string(bundle.HeapDump) != "FAKE_HEAP_DUMP" {
		t.Fatalf("heap dump = %q", bundle.HeapDump)
	}
	if string(bundle.GoroutineProfile) != "FAKE_GOROUTINE_PROFILE" {
		t.Fatalf("goroutine profile = %q", bundle.GoroutineProfile)
	}
	if bundle.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("format = %q", bundle.Metadata.Format)
	}
	if !bundle.Metadata.GCBeforeHeapDump {
		t.Fatal("expected gc_before_heap_dump to default to true")
	}
}

func TestHandlerGCQueryParameter(t *testing.T) {
	tests := []struct {
		target string
		wantGC bool
	}{
		{target: snapshotPath, wantGC: true},
		{target: snapshotPath + "?gc=0", wantGC: false},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)

			handler(testCaptureOptions(nil, nil)).ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes()))
			if err != nil {
				t.Fatalf("read snapshot: %v", err)
			}
			if bundle.Metadata.GCBeforeHeapDump != tt.wantGC {
				t.Fatalf("gc_before_heap_dump = %t, want %t", bundle.Metadata.GCBeforeHeapDump, tt.wantGC)
			}
		})
	}
}

func TestHandlerWriterFailures(t *testing.T) {
	tests := []struct {
		name       string
		heapErr    error
		profileErr error
		want       string
	}{
		{name: "heap", heapErr: errors.New("heap failed"), want: "write heap dump"},
		{name: "profile", profileErr: errors.New("profile failed"), want: "write goroutine profile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)

			handler(testCaptureOptions(tt.heapErr, tt.profileErr)).ServeHTTP(rr, req)

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("body = %q, want substring %q", rr.Body.String(), tt.want)
			}
			if _, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes())); err == nil {
				t.Fatal("error response unexpectedly parsed as a valid snapshot")
			}
		})
	}
}

func testCaptureOptions(heapErr, profileErr error) capture.CaptureOptions {
	return capture.CaptureOptions{
		GCBeforeHeapDump: true,
		HeapDumpWriter: fakeHeapDumpWriter{
			data: []byte("FAKE_HEAP_DUMP"),
			err:  heapErr,
		},
		GoroutineProfileWriter: fakeGoroutineProfileWriter{
			data: []byte("FAKE_GOROUTINE_PROFILE"),
			err:  profileErr,
		},
		MetadataProvider: fakeMetadataProvider{},
		GC:               func() {},
	}
}

type fakeHeapDumpWriter struct {
	data []byte
	err  error
}

func (w fakeHeapDumpWriter) WriteHeapDump(fd uintptr) error {
	if w.err != nil {
		return w.err
	}
	f := os.NewFile(fd, snapshot.HeapDumpFile)
	if f == nil {
		return errors.New("invalid file descriptor")
	}
	if _, err := f.Write(w.data); err != nil {
		return err
	}
	runtime.KeepAlive(f)
	return nil
}

type fakeGoroutineProfileWriter struct {
	data []byte
	err  error
}

func (w fakeGoroutineProfileWriter) WriteGoroutineProfile(out io.Writer) error {
	if w.err != nil {
		return w.err
	}
	_, err := out.Write(w.data)
	return err
}

type fakeMetadataProvider struct{}

func (fakeMetadataProvider) Metadata(gcBeforeHeapDump bool) snapshot.SnapshotMetadata {
	return snapshot.SnapshotMetadata{
		Format:               snapshot.FormatV1,
		CreatedAt:            time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		GoVersion:            "go-test",
		PID:                  12345,
		HeapDumpFile:         snapshot.HeapDumpFile,
		GoroutineProfileFile: snapshot.GoroutineProfileFile,
		GCBeforeHeapDump:     gcBeforeHeapDump,
	}
}
