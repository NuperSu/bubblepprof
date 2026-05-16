package bubblepprof

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"bubblepprof/internal/goid"
	"bubblepprof/internal/snapshot"
)

func TestLabelSetMapAndLen(t *testing.T) {
	ls := Labels("bubble", "alpha", "job", "42")
	if got := ls.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	want := map[string]string{"bubble": "alpha", "job": "42"}
	if got := ls.Map(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Map = %v, want %v", got, want)
	}
}

func TestLabelsEmpty(t *testing.T) {
	ls := Labels()
	if ls.Len() != 0 {
		t.Fatalf("Len = %d", ls.Len())
	}
	if ls.Map() != nil && len(ls.Map()) != 0 {
		t.Fatalf("Map = %v", ls.Map())
	}
}

func TestRegistryReturnsActive(t *testing.T) {
	r := withFreshRegistry(t)
	if Registry() != r {
		t.Fatalf("Registry() != the registry we installed")
	}
}

func TestSetGoroutineLabelsRecordsRegistry(t *testing.T) {
	r := withFreshRegistry(t)
	id, ok := goid.CurrentGoroutineID()
	if !ok {
		t.Skip("goroutine ID unavailable")
	}
	t.Cleanup(func() { r.Clear(id) })

	// Build a context with pprof labels by entering and capturing.
	var capturedCtx context.Context
	Do(context.Background(), Labels("bubble", "alpha"), func(ctx context.Context) {
		capturedCtx = ctx
	})
	if capturedCtx == nil {
		t.Fatal("expected context from Do")
	}

	r.Clear(id)
	SetGoroutineLabels(capturedCtx)
	got := r.Lookup(id)
	if !reflect.DeepEqual(got, map[string]string{"bubble": "alpha"}) {
		t.Fatalf("registry after SetGoroutineLabels = %v", got)
	}
}

func TestLabelsFromContextNil(t *testing.T) {
	if got := labelsFromContext(nil); got != nil {
		t.Fatalf("labelsFromContext(nil) = %v, want nil", got)
	}
}

func TestHandlerDefault(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
	Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Handler() default status = %d, body=%s", rr.Code, rr.Body.String())
	}
	bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if bundle.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("format = %q", bundle.Metadata.Format)
	}
}

func TestHandlerWithOptionsDisableEverything(t *testing.T) {
	r := withFreshRegistry(t)
	// Add a manifest entry so labels.json would normally be emitted.
	r.Set(42, map[string]string{"bubble": "alpha"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
	HandlerWithOptions(Options{
		GCBeforeHeapDump:     false,
		DisableLabelManifest: true,
		DisableDiagnostics:   true,
	}).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if bundle.Labels != nil {
		t.Fatalf("expected no labels.json when DisableLabelManifest is set, got %d bytes", len(bundle.Labels))
	}
	if bundle.GoroutineStacks != nil {
		t.Fatalf("expected no goroutine.stacks when DisableDiagnostics is set, got %d bytes", len(bundle.GoroutineStacks))
	}
	if bundle.Metadata.GCBeforeHeapDump {
		t.Fatalf("expected gc_before_heap_dump=false")
	}
}

func TestHandlerWithOptionsZeroValueIncludesManifestAndDiagnostics(t *testing.T) {
	r := withFreshRegistry(t)
	r.Set(42, map[string]string{"bubble": "alpha"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
	HandlerWithOptions(Options{}).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("ReadSnapshotBundle: %v", err)
	}
	if bundle.Labels == nil {
		t.Fatal("expected labels.json in Options{} default; registry was non-empty")
	}
	if bundle.GoroutineStacks == nil {
		t.Fatal("expected goroutine.stacks in Options{} default (diagnostics on)")
	}
}

func TestRegisterMountsSnapshotPath(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Register mux status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerRejectsNonGET(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, snapshotPath, nil)
	Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q", got)
	}
}
