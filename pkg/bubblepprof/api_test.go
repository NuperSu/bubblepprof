package bubblepprof

import (
	"context"
	"reflect"
	"runtime/pprof"
	"testing"
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

func TestSetGoroutineLabelsPropagatesContext(t *testing.T) {
	// Capture a labeled context from Do.
	var capturedCtx context.Context
	Do(context.Background(), Labels("bubble", "alpha"), func(ctx context.Context) {
		capturedCtx = ctx
	})
	if capturedCtx == nil {
		t.Fatal("expected context from Do")
	}

	// SetGoroutineLabels is a thin wrapper; verify it does not panic and
	// that the context it received still carries the expected labels.
	SetGoroutineLabels(capturedCtx)
	got := make(map[string]string)
	pprof.ForLabels(capturedCtx, func(k, v string) bool {
		got[k] = v
		return true
	})
	if got["bubble"] != "alpha" {
		t.Fatalf("context labels: bubble = %q, want alpha", got["bubble"])
	}
}
