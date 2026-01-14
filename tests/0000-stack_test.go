package tests

import "testing"

type S0000 struct {
	x *int
}

func Test_0000_stack(t *testing.T) {
	ctx := prepareGoroutine(t)

	x := 0
	s := S0000{x: &x}

	// TODO: make a heap snapshot and validate it.
	// For now, insert a SIGSEGV here to produce a core dump.

	_ = ctx
	_ = s
}
