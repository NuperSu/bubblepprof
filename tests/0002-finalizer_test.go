package tests

import (
	"runtime"
	"testing"
)

type S0002Obj struct {
	data []byte
}

type S0002 struct {
	obj *S0002Obj
}

func Test_0002_finalizer(t *testing.T) {
	ctx := prepareGoroutine(t)

	obj := &S0002Obj{data: []byte("finalizer-target")}
	runtime.SetFinalizer(obj, func(o *S0002Obj) {
		_ = len(o.data)
	})

	s := S0002{obj: obj}

	// TODO: make a heap snapshot and validate finalizer queue references are traversed.
	runtime.KeepAlive(obj)
	_ = ctx
	_ = s
}
