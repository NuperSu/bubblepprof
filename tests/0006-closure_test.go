package tests

import "testing"

type S0006Node struct {
	payload string
}

type S0006 struct {
	fn func() int
}

func Test_0006_closure(t *testing.T) {
	ctx := prepareGoroutine(t)

	n := &S0006Node{payload: "captured"}
	count := 0
	fn := func() int {
		if n.payload != "" {
			count++
		}
		return count
	}

	s := S0006{fn: fn}

	// TODO: add a matching fixture snapshot and decide whether closure
	// environments are in scope; current traversal treats funcs as runtime
	// pointers and does not decode captured variables.
	_ = ctx
	_ = s
}
