package main

import (
	"fmt"
	"os"

	"bubblepprof/internal/snapshotinfo"
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "snapshot" {
		return snapshotinfo.Run(os.Stdout, os.Stderr, os.Args[0], args[1:])
	}

	fmt.Fprintf(os.Stderr, "usage:\n")
	fmt.Fprintf(os.Stderr, "  %s snapshot info snapshot.tar\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s snapshot parse snapshot.tar\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s snapshot graph snapshot.tar\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s snapshot heap-labels snapshot.tar\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s snapshot labels snapshot.tar\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s snapshot bubbles snapshot.tar\n", os.Args[0])
	return 2
}
