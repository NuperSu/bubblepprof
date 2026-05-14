package tests

import (
	"runtime"
	"testing"
)

type S0009Node struct {
	child *S0009Node
}

type S0009 struct {
	root *S0009Node
}

func Test_0009_finalizer_chain(t *testing.T) {
	ctx := prepareGoroutine(t)

	root := &S0009Node{}
	root.child = &S0009Node{}
	runtime.SetFinalizer(root, func(n *S0009Node) {
		_ = n.child
	})

	s := S0009{root: root}

	// TODO: add a matching fixture snapshot and verify finalizer reachability
	// after runtime finalizer queues are decoded or otherwise exposed reliably.
	runtime.KeepAlive(root)
	_ = ctx
	_ = s
}
