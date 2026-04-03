package tests

import (
	"bytes"
	"testing"
)

type S0001Node struct {
	value int
	next  *S0001Node
}

type S0001 struct {
	ch chan *S0001Node
}

func Test_0001_channel(t *testing.T) {
	_ = prepareGoroutine(t)

	heap0001 := readHeapDumpFixture(t, "HEAP_0001_PATH", "../heap-snapshots/heap-0001.out")
	heap0000 := readHeapDumpFixture(t, "HEAP_0000_PATH", "../heap-snapshots/heap-0000.out")

	if len(heap0001) < 64*1024 {
		t.Fatalf("expected substantial heap dump for 0001 fixture, got %d bytes", len(heap0001))
	}
	if bytes.Equal(heap0000, heap0001) {
		t.Fatalf("expected distinct dumps for 0000 and 0001 fixtures")
	}
}
