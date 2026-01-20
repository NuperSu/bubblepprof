package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"

	"delve_first_project/goheap"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"
)

type options struct {
	exePath  string
	corePath string
	depth    int // stack depth per goroutine
	pageSize int // goroutine page size for debugger.Goroutines(start,count)

	// Delve variable loading limits (these affect how much proc.Variable.Children is populated).
	maxRecurse int
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
	flag.IntVar(&o.depth, "depth", 64, "stack depth per goroutine")
	flag.IntVar(&o.pageSize, "page", 256, "goroutine page size for debugger.Goroutines(start,count)")
	flag.IntVar(&o.maxRecurse, "maxrecurse", 64, "Delve variable recursion limit (MaxVariableRecurse)")

	flag.Usage = func() {
		fmt.Fprintf(errOut, "usage: %s -exe /path/to/bin -core /path/to/core [flags]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if o.exePath == "" || o.corePath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if o.depth <= 0 {
		o.depth = 64
	}
	if o.pageSize <= 0 {
		o.pageSize = 256
	}
	if o.maxRecurse <= 0 {
		o.maxRecurse = 64
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

	// FollowPointers=true so proc.Variable.Children includes dereferenced pointees.
	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: o.maxRecurse,
		MaxStringLen:       256,
		MaxArrayValues:     64,
		MaxStructFields:    128,
	}

	// Traversal-only object graph builder (no printing inside it).
	live := goheap.New(d)

	for _, g := range gs {
		printGoroutineHeader(out, g)

		frames, err := d.Stacktrace(g.ID, o.depth, api.StacktraceOptions(0))
		if err != nil {
			fmt.Fprintf(out, "  stacktrace error: %v\n", err)
			continue
		}

		for i, fr := range frames {
			printFrameHeader(out, i, fr)

			locals, err := d.LocalVariables(g.ID, i, 0, loadCfg)
			if err != nil {
				fmt.Fprintf(out, "     locals error: %v\n", err)
				continue
			}
			for _, v := range locals {
				live.Add(v)
			}

			// Best-effort: include package-scope variables (helps pull in globals such
			// as channels, and potentially runtime/global structures).
			if pkgVars, err := callVarsMethod(d, "PackageVariables", g.ID, i, loadCfg); err == nil {
				for _, v := range pkgVars {
					live.Add(v)
				}
			}

			// Best-effort: attempt to pull in runtime finalizer structures by name.
			// This is intentionally conservative: failures are ignored.
			for _, expr := range []string{"runtime.finq", "runtime.allfin", "runtime.allfins", "runtime.fing"} {
				if v, err := callEvalMethod(d, g.ID, i, expr, loadCfg); err == nil && v != nil {
					live.Add(v)
				}
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

// callVarsMethod uses reflection so this binary can build against Delve versions
// that may not expose every helper (e.g., PackageVariables).
func callVarsMethod(d *debugger.Debugger, method string, gid, frame int, cfg proc.LoadConfig) ([]*proc.Variable, error) {
	m := reflect.ValueOf(d).MethodByName(method)
	if !m.IsValid() {
		return nil, fmt.Errorf("%s not supported", method)
	}

	vals := m.Call([]reflect.Value{
		reflect.ValueOf(gid),
		reflect.ValueOf(frame),
		reflect.ValueOf(0),
		reflect.ValueOf(cfg),
	})
	if len(vals) != 2 {
		return nil, fmt.Errorf("unexpected %s signature", method)
	}

	if !vals[1].IsNil() {
		err, _ := vals[1].Interface().(error)
		if err == nil {
			err = fmt.Errorf("%s returned non-nil error", method)
		}
		return nil, err
	}

	if vals[0].IsNil() {
		return nil, nil
	}

	out := make([]*proc.Variable, 0)
	s, ok := vals[0].Interface().([]*proc.Variable)
	if ok {
		out = append(out, s...)
		return out, nil
	}

	return nil, fmt.Errorf("unexpected %s return type", method)
}

func callEvalMethod(d *debugger.Debugger, gid, frame int, expr string, cfg proc.LoadConfig) (*proc.Variable, error) {
	m := reflect.ValueOf(d).MethodByName("EvalVariable")
	if !m.IsValid() {
		return nil, fmt.Errorf("EvalVariable not supported")
	}

	vals := m.Call([]reflect.Value{
		reflect.ValueOf(gid),
		reflect.ValueOf(frame),
		reflect.ValueOf(expr),
		reflect.ValueOf(cfg),
	})
	if len(vals) != 2 {
		return nil, fmt.Errorf("unexpected EvalVariable signature")
	}

	if !vals[1].IsNil() {
		err, _ := vals[1].Interface().(error)
		if err == nil {
			err = fmt.Errorf("EvalVariable returned non-nil error")
		}
		return nil, err
	}
	if vals[0].IsNil() {
		return nil, nil
	}
	v, ok := vals[0].Interface().(*proc.Variable)
	if !ok {
		return nil, fmt.Errorf("unexpected EvalVariable return type")
	}
	return v, nil
}
