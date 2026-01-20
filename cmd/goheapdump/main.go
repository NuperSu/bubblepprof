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
	fmt.Fprintf(out, "goroutines: %d\n\n", len(gs))

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

	// Choose a stable evaluation context for best-effort global roots.
	// (Package variables and runtime globals are process-wide; we only need to add them once.)
	var ctxGID int64
	if len(gs) > 0 {
		ctxGID = gs[0].ID
	}

	// Best-effort: add package-scope variables once.
	// Do this outside the per-frame loop to avoid repeated huge allocations.
	if ctxGID != 0 {
		if pkgVars, err := callVarsMethod(d, "PackageVariables", ctxGID, 0, loadCfg); err == nil {
			for _, v := range pkgVars {
				live.Add(v)
			}
		}
	}

	// Best-effort: attempt to pull in runtime finalizer structures by name once.
	// Failures are ignored.
	if ctxGID != 0 {
		for _, expr := range []string{"runtime.finq", "runtime.allfin", "runtime.allfins", "runtime.fing"} {
			if v, err := callEvalMethod(d, ctxGID, 0, expr, loadCfg); err == nil && v != nil {
				live.Add(v)
			}
		}
	}

	for _, g := range gs {
		printGoroutineHeader(out, g)

		frames, err := d.Stacktrace(g.ID, o.depth, api.StacktraceOptions(0))
		if err != nil {
			fmt.Fprintf(out, "  stacktrace error: %v\n\n", err)
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

			fmt.Fprintf(out, "     (live objects so far: %d)\n", live.Count())
		}

		fmt.Fprintln(out)
	}

	return nil
}

// callVarsMethod uses reflection so this binary can build against Delve versions
// that may not expose every helper (e.g., PackageVariables).
func callVarsMethod(d *debugger.Debugger, method string, gid int64, frame int, cfg proc.LoadConfig) ([]*proc.Variable, error) {
	m := reflect.ValueOf(d).MethodByName(method)
	if !m.IsValid() {
		return nil, fmt.Errorf("%s not supported", method)
	}

	args, err := buildReflectArgs(m.Type(), gid, frame, "", cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}

	vals := m.Call(args)
	if len(vals) != 2 {
		return nil, fmt.Errorf("unexpected %s signature", method)
	}

	if !vals[1].IsNil() {
		if e, _ := vals[1].Interface().(error); e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("%s returned non-nil error", method)
	}

	if vals[0].IsNil() {
		return nil, nil
	}

	if s, ok := vals[0].Interface().([]*proc.Variable); ok {
		return s, nil
	}
	return nil, fmt.Errorf("unexpected %s return type", method)
}

func callEvalMethod(d *debugger.Debugger, gid int64, frame int, expr string, cfg proc.LoadConfig) (*proc.Variable, error) {
	m := reflect.ValueOf(d).MethodByName("EvalVariable")
	if !m.IsValid() {
		return nil, fmt.Errorf("EvalVariable not supported")
	}

	args, err := buildReflectArgs(m.Type(), gid, frame, expr, cfg)
	if err != nil {
		return nil, fmt.Errorf("EvalVariable: %w", err)
	}

	vals := m.Call(args)
	if len(vals) != 2 {
		return nil, fmt.Errorf("unexpected EvalVariable signature")
	}

	if !vals[1].IsNil() {
		if e, _ := vals[1].Interface().(error); e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("EvalVariable returned non-nil error")
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

// buildReflectArgs matches parameters by type and order.
// Supported parameter kinds:
//   - int64: goroutine id
//   - int: frame index and/or extra int args (defaults to 0)
//   - string: expression (EvalVariable)
//   - proc.LoadConfig: load config
func buildReflectArgs(fn reflect.Type, gid int64, frame int, expr string, cfg proc.LoadConfig) ([]reflect.Value, error) {
	int64T := reflect.TypeOf(int64(0))
	intT := reflect.TypeOf(int(0))
	stringT := reflect.TypeOf("")
	loadCfgT := reflect.TypeOf(proc.LoadConfig{})

	usedFrame := false
	usedExpr := false

	args := make([]reflect.Value, 0, fn.NumIn())
	for i := 0; i < fn.NumIn(); i++ {
		t := fn.In(i)

		switch {
		case t == int64T:
			args = append(args, reflect.ValueOf(gid).Convert(t))

		case t == intT:
			if !usedFrame {
				args = append(args, reflect.ValueOf(frame).Convert(t))
				usedFrame = true
			} else {
				args = append(args, reflect.ValueOf(0).Convert(t))
			}

		case t == stringT:
			if usedExpr {
				return nil, fmt.Errorf("multiple string params not supported")
			}
			args = append(args, reflect.ValueOf(expr).Convert(t))
			usedExpr = true

		case t == loadCfgT:
			args = append(args, reflect.ValueOf(cfg).Convert(t))

		default:
			// Allow compatible LoadConfig aliases if any.
			v := reflect.ValueOf(cfg)
			if v.Type().ConvertibleTo(t) {
				args = append(args, v.Convert(t))
				continue
			}
			return nil, fmt.Errorf("unsupported param %d type %s", i, t.String())
		}
	}

	return args, nil
}
