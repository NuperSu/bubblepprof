package tests

import "testing"

type S0005Item struct {
	value int
}

type S0005 struct {
	items []*S0005Item
}

func Test_0005_slice(t *testing.T) {
	ctx := prepareGoroutine(t)

	s := S0005{
		items: []*S0005Item{
			{value: 10},
			{value: 20},
			{value: 30},
		},
	}

	// TODO: add a matching fixture snapshot and verify that goheap follows
	// pointers stored in slice elements, subject to MaxArrayValues.
	_ = ctx
	_ = s
}
