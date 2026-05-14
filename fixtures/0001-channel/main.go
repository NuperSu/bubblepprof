package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

type S0001Node struct {
	value int
	next  *S0001Node
}

type S0001 struct {
	ch chan *S0001Node
}

func main() {
	mode := flag.String("mode", "heapdump", "output mode: heapdump or core")
	heapdumpPath := flag.String("heapdump", "heap-0001.out", "path for heap dump output")
	pause := flag.Duration("pause", 0, "optional pause before output/crash (e.g. 3s)")
	flag.Parse()

	a := &S0001Node{value: 1}
	b := &S0001Node{value: 2}
	a.next = b

	ch := make(chan *S0001Node, 4)
	ch <- a
	ch <- b

	s := S0001{ch: ch}

	if *pause > 0 {
		fmt.Printf("fixture 0001: pid=%d sleeping for %s\n", os.Getpid(), pause.String())
		time.Sleep(*pause)
	}

	switch *mode {
	case "heapdump":
		if err := writeHeapDump(*heapdumpPath); err != nil {
			fmt.Fprintf(os.Stderr, "write heapdump: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("fixture 0001: heap dump written to %s\n", *heapdumpPath)

	case "core":
		runtime.KeepAlive(s)
		runtime.KeepAlive(a)
		runtime.KeepAlive(b)
		fmt.Printf("fixture 0001: pid=%d aborting for core dump (ensure ulimit -c is enabled)\n", os.Getpid())
		_ = syscall.Kill(os.Getpid(), syscall.SIGABRT)
		time.Sleep(500 * time.Millisecond)
		os.Exit(2)

	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (expected heapdump or core)\n", *mode)
		os.Exit(2)
	}

	runtime.KeepAlive(s)
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
}

func writeHeapDump(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	debug.WriteHeapDump(f.Fd())
	return nil
}
