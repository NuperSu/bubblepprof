package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"

	"delve_first_project/goheap"
)

type options struct {
	exePath  string
	corePath string
	pageSize int // goroutine page size for debugger.Goroutines(start,count)

	// Delve materialization limits per single fetch.
	// Graph traversal in goheap is unbounded (it re-evaluates lazily).
	maxStringLen    int
	maxArrayValues  int
	maxStructFields int
}

func main() {
	os.Exit(realMain(os.Stdout, os.Stderr))
}

func realMain(out, errOut io.Writer) int {
	opts := parseFlags(errOut)

	if err := run(out, opts); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func parseFlags(errOut io.Writer) options {
	var o options

	flag.StringVar(&o.exePath, "exe", "", "path to the executable that produced the core (must match)")
	flag.StringVar(&o.corePath, "core", "", "path to the core dump")
	flag.IntVar(&o.pageSize, "page", 256, "goroutine page size for debugger.Goroutines(start,count)")

	// Delve variable materialization limits.
	flag.IntVar(&o.maxStringLen, "maxstr", 256, "Delve MaxStringLen")
	flag.IntVar(&o.maxArrayValues, "maxarr", 1024, "Delve MaxArrayValues")
	flag.IntVar(&o.maxStructFields, "maxfields", 1024, "Delve MaxStructFields")

	flag.Usage = func() {
		fmt.Fprintf(errOut, "usage: %s -exe /path/to/bin -core /path/to/core [flags]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if o.exePath == "" || o.corePath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if o.pageSize <= 0 {
		o.pageSize = 256
	}
	if o.maxStringLen <= 0 {
		o.maxStringLen = 256
	}
	if o.maxArrayValues <= 0 {
		o.maxArrayValues = 1024
	}
	if o.maxStructFields <= 0 {
		o.maxStructFields = 1024
	}

	return o
}

func run(out io.Writer, o options) error {
	cfg := &debugger.Config{
		CoreFile:       o.corePath,
		Backend:        "default",
		CheckGoVersion: true,
		ExecuteKind:    debugger.ExecutingExistingFile,
	}

	d, err := debugger.New(cfg, []string{o.exePath})
	if err != nil {
		return fmt.Errorf("debugger.New: %w", err)
	}
	defer func() { _ = d.Detach(false) }()

	gs, err := listAllGoroutines(d, o.pageSize)
	if err != nil {
		return fmt.Errorf("list goroutines: %w", err)
	}
	fmt.Fprintf(out, "goroutines: %d\n", len(gs))

	// MaxVariableRecurse is kept at 1 so that a single LocalVariables call
	// never explodes into an exponential tree (cyclic/fan-out structures).
	// goheap lazily re-evaluates pointers it discovers, with deduplication,
	// so the full graph is still traversed.
	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 1,
		MaxStringLen:       o.maxStringLen,
		MaxArrayValues:     o.maxArrayValues,
		MaxStructFields:    o.maxStructFields,
	}

	live := goheap.New(d, loadCfg)

	for _, g := range gs {
		printGoroutineHeader(out, g)

		const maxDepth = 8192
		frames, err := d.Stacktrace(g.ID, maxDepth, api.StacktraceOptions(0)) // temporary limit, later will read it all in chunks
		if err != nil {
			fmt.Fprintf(out, "  stacktrace error: %v\n", err)
			continue
		}

		if len(frames) == maxDepth {
			fmt.Fprintf(out, "  note: stacktrace truncated at %d frames\n", maxDepth)
		}

		for i, fr := range frames {
			printFrameHeader(out, i, fr)

			locals, err := d.LocalVariables(g.ID, i, 0, loadCfg)
			if err != nil {
				fmt.Fprintf(out, "     locals error: %v\n", err)
				continue
			}

			for _, v := range locals {
				live.Add(v, g.ID, i)
			}

			n := 0
			for range live.All() {
				n++
			}
			fmt.Fprintf(out, "     (live objects so far: %d)\n", n)
		}
	}

	return nil
}
