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

	// TODO: make a heap snapshot and validate closure captures are traversed.
	_ = ctx
	_ = s
}
