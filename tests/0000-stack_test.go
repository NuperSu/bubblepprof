package tests

import "testing"

type S0000 struct {
	x *int
}

func Test_0000_stack(t *testing.T) {
	_ = prepareGoroutine(t)

	heap := readHeapDumpFixture(t, "HEAP_0000_PATH", "../heap-snapshots/heap-0000.out")
	if len(heap) < 64*1024 {
		t.Fatalf("expected substantial heap dump for 0000 fixture, got %d bytes", len(heap))
	}
}
