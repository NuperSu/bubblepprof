package tests

import "testing"

type S0004Leaf struct {
	id int
}

type S0004 struct {
	v any
}

func Test_0004_interface(t *testing.T) {
	ctx := prepareGoroutine(t)

	leaf := &S0004Leaf{id: 42}
	s := S0004{v: leaf}

	// TODO: make a heap snapshot and validate interface-contained pointers are traversed.
	_ = ctx
	_ = s
}
