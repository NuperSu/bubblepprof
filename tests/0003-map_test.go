package tests

import "testing"

type S0003Node struct {
	name string
	next *S0003Node
}

type S0003 struct {
	m map[string]*S0003Node
}

func Test_0003_map(t *testing.T) {
	_ = prepareGoroutine(t)

	heap := readHeapDumpFixture(t, "HEAP_0003_PATH", "../heap-snapshots/heap-0003.out")
	if len(heap) < 64*1024 {
		t.Fatalf("expected substantial heap dump for 0003 fixture, got %d bytes", len(heap))
	}
}
