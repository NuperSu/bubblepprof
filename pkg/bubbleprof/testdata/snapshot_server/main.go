package main

import (
	"fmt"
	"net"
	"net/http"

	"bubbleprof/pkg/bubbleprof"
)

func main() {
	mux := http.NewServeMux()
	bubbleprof.Register(mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Println(ln.Addr().String())

	if err := http.Serve(ln, mux); err != nil {
		panic(err)
	}
}
