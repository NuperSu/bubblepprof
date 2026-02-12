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

type S0002Obj struct {
	data []byte
}

type S0002 struct {
	obj *S0002Obj
}

func main() {
	mode := flag.String("mode", "heapdump", "output mode: heapdump or core")
	heapdumpPath := flag.String("heapdump", "heap-0002.out", "path for heap dump output")
	pause := flag.Duration("pause", 0, "optional pause before output/crash (e.g. 3s)")
	flag.Parse()

	obj := &S0002Obj{data: []byte("finalizer-target")}
	runtime.SetFinalizer(obj, func(o *S0002Obj) {
		_ = len(o.data)
	})

	s := S0002{obj: obj}

	if *pause > 0 {
		fmt.Printf("fixture 0002: pid=%d sleeping for %s\n", os.Getpid(), pause.String())
		time.Sleep(*pause)
	}

	switch *mode {
	case "heapdump":
		if err := writeHeapDump(*heapdumpPath); err != nil {
			fmt.Fprintf(os.Stderr, "write heapdump: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("fixture 0002: heap dump written to %s\n", *heapdumpPath)

	case "core":
		runtime.KeepAlive(s)
		runtime.KeepAlive(obj)
		fmt.Printf("fixture 0002: pid=%d aborting for core dump (ensure ulimit -c is enabled)\n", os.Getpid())
		_ = syscall.Kill(os.Getpid(), syscall.SIGABRT)
		time.Sleep(500 * time.Millisecond)
		os.Exit(2)

	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (expected heapdump or core)\n", *mode)
		os.Exit(2)
	}

	runtime.KeepAlive(s)
	runtime.KeepAlive(obj)
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
