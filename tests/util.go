package tests

import (
	"context"
	"runtime/pprof"
	"testing"
)

func prepareGoroutine(t *testing.T) context.Context {
	t.Helper()

	ctx := pprof.WithLabels(t.Context(), pprof.Labels("test", t.Name()))
	pprof.SetGoroutineLabels(ctx)
	return ctx
}
