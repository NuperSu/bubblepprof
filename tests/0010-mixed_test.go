package tests

import (
	"runtime"
	"testing"
)

type S0010Inner struct {
	name string
	next *S0010Inner
}

type S0010 struct {
	ch   chan *S0010Inner
	data map[string]any
}

func Test_0010_mixed(t *testing.T) {
	ctx := prepareGoroutine(t)

	a := &S0010Inner{name: "a"}
	b := &S0010Inner{name: "b", next: a}
	a.next = b

	ch := make(chan *S0010Inner, 2)
	ch <- a
	ch <- b

	runtime.SetFinalizer(a, func(n *S0010Inner) {
		_ = n.next
	})

	s := S0010{
		ch: ch,
		data: map[string]any{
			"head": a,
			"tail": b,
		},
	}

	// TODO: add a matching fixture snapshot and verify mixed references once
	// channel buffers, map buckets, and finalizer queues are handled explicitly.
	runtime.KeepAlive(a)
	_ = ctx
	_ = s
}
