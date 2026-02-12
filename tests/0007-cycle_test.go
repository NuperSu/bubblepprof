package tests

import "testing"

type S0007Node struct {
	name string
	next *S0007Node
}

type S0007 struct {
	head *S0007Node
}

func Test_0007_cycle(t *testing.T) {
	ctx := prepareGoroutine(t)

	a := &S0007Node{name: "a"}
	b := &S0007Node{name: "b"}
	a.next = b
	b.next = a

	s := S0007{head: a}

	// TODO: make a heap snapshot and validate cyclic references are deduplicated.
	_ = ctx
	_ = s
}
