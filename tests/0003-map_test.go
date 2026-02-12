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
	ctx := prepareGoroutine(t)

	n1 := &S0003Node{name: "n1"}
	n2 := &S0003Node{name: "n2", next: n1}

	s := S0003{
		m: map[string]*S0003Node{
			"a": n1,
			"b": n2,
		},
	}

	// TODO: make a heap snapshot and validate map element references are traversed.
	_ = ctx
	_ = s
}
