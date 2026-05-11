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

	// TODO: add a matching fixture snapshot and verify that goheap follows the
	// concrete pointer stored inside an interface value.
	_ = ctx
	_ = s
}
