package bubblepprof

import (
	"context"
	"reflect"
	"testing"

	"bubblepprof/internal/goid"
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
