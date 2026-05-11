package tests

import "testing"

type S0008Payload struct {
	buf []byte
}

type S0008 struct {
	ch chan any
}

func Test_0008_channel_interface(t *testing.T) {
	ctx := prepareGoroutine(t)

	p1 := &S0008Payload{buf: []byte("alpha")}
	p2 := &S0008Payload{buf: []byte("beta")}

	ch := make(chan any, 3)
	ch <- p1
	ch <- p2

	s := S0008{ch: ch}

	// TODO: add a matching fixture snapshot and implement channel buffer
	// decoding before expecting payloads queued in ch to be reachable.
	_ = ctx
	_ = s
}
