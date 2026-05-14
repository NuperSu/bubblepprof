package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/go-delve/delve/service/debugger"
)

type options struct {
	exePath  string
	corePath string
	pageSize int // Batch size for debugger.Goroutines(start, count).

	// Delve materialization limits for each variable fetch. They affect how
	// many children Delve returns for strings, arrays/slices, and structs.
	maxStringLen    int
	maxArrayValues  int
	maxStructFields int

	showGoroutines bool
}

func main() {
	os.Exit(realMain(os.Stdout, os.Stderr))
}

func realMain(out, errOut io.Writer) int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "snapshot" {
		return runSnapshotCommand(out, errOut, args[1:])
	}

	opts, err := parseFlags(args, errOut)
	if err != nil {
		if errors.Is(err, errInvalidUsage) {
			return 2
		}
		fmt.Fprintln(errOut, err)
		return 1
	}

	if err := run(out, opts); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

var errInvalidUsage = errors.New("invalid usage")

func parseFlags(args []string, errOut io.Writer) (options, error) {
	var o options

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)

	fs.StringVar(&o.exePath, "exe", "", "path to the executable that produced the core (must match)")
	fs.StringVar(&o.corePath, "core", "", "path to the core dump")
	fs.IntVar(&o.pageSize, "page", 256, "goroutine page size for debugger.Goroutines(start,count)")

	// Limits for one Delve variable read, not global graph limits.
	fs.IntVar(&o.maxStringLen, "maxstr", 256, "Delve MaxStringLen")
	fs.IntVar(&o.maxArrayValues, "maxarr", 1024, "Delve MaxArrayValues")
	fs.IntVar(&o.maxStructFields, "maxfields", 1024, "Delve MaxStructFields")
	fs.BoolVar(&o.showGoroutines, "goroutines", false, "include per-goroutine object details")

	fs.Usage = func() {
		fmt.Fprintf(errOut, "usage: %s -exe /path/to/bin -core /path/to/core [flags]\n\n", os.Args[0])
		fmt.Fprintf(errOut, "       %s snapshot info snapshot.tar\n", os.Args[0])
		fmt.Fprintf(errOut, "       %s snapshot parse snapshot.tar\n", os.Args[0])
		fmt.Fprintf(errOut, "       %s snapshot graph snapshot.tar\n\n", os.Args[0])
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}

	if o.exePath == "" || o.corePath == "" {
		fs.Usage()
		return o, errInvalidUsage
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

	return o, nil
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

	result, err := buildHeapGraph(d, o)
	if err != nil {
		return err
	}

	printAnalysisReport(out, result, o)
	return nil
}
