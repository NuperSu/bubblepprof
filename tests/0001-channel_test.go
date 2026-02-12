package tests

import "testing"

type S0001Node struct {
	value int
	next  *S0001Node
}

type S0001 struct {
	ch chan *S0001Node
}

func Test_0001_channel(t *testing.T) {
	ctx := prepareGoroutine(t)

	a := &S0001Node{value: 1}
	b := &S0001Node{value: 2}
	a.next = b

	ch := make(chan *S0001Node, 4)
	ch <- a
	ch <- b

	s := S0001{ch: ch}

	// TODO: make a heap snapshot and validate that channel-backed references are traversed.
	_ = ctx
	_ = s
}
