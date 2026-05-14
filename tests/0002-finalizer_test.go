package tests

import "testing"

type S0002Obj struct {
	data []byte
}

type S0002 struct {
	obj *S0002Obj
}

func Test_0002_finalizer(t *testing.T) {
	_ = prepareGoroutine(t)

	heap := readHeapDumpFixture(t, "HEAP_0002_PATH", "../heap-snapshots/heap-0002.out")
	if len(heap) < 64*1024 {
		t.Fatalf("expected substantial heap dump for 0002 fixture, got %d bytes", len(heap))
	}
}
