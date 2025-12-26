package main

import (
	"flag"
	"fmt"
	"go/constant"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"
)

type options struct {
	exePath  string
	corePath string
	depth    int
	pageSize int
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

	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 1,
		MaxStringLen:       256,
		MaxArrayValues:     64,
		MaxStructFields:    64,
	}

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

			ptrs := pointerLocals(locals)
			if len(ptrs) == 0 {
				fmt.Fprintf(out, "     (no pointer-typed locals)\n")
				continue
			}

			for _, v := range ptrs {
				printPointerVar(out, v)
			}
		}
	}

	return nil
}

func listAllGoroutines(d *debugger.Debugger, pageSize int) ([]*proc.G, error) {
	var (
		out   []*proc.G
		start int
	)

	for {
		page, total, err := d.Goroutines(start, pageSize)
		if err != nil {
			return nil, err
		}

		out = append(out, page...)
		start += len(page)

		if len(page) == 0 || start >= total {
			return out, nil
		}
	}
}

func printGoroutineHeader(w io.Writer, g *proc.G) {
	// proc.G.Status / WaitReason are numeric runtime values; still useful.
	if g.Unreadable != nil {
		fmt.Fprintf(w, "\n== goroutine %d (unreadable: %v)\n", g.ID, g.Unreadable)
		return
	}
	fmt.Fprintf(w, "\n== goroutine %d status=%d waitReason=%d\n", g.ID, g.Status, g.WaitReason)
}

func printFrameHeader(w io.Writer, idx int, fr proc.Stackframe) {
	desc := formatFrame(fr)
	if fr.Err != nil {
		fmt.Fprintf(w, "  -- frame %d: %s (frame error: %v)\n", idx, desc, fr.Err)
		return
	}
	fmt.Fprintf(w, "  -- frame %d: %s\n", idx, desc)
}

func formatFrame(fr proc.Stackframe) string {
	// Prefer Call, but fall back to Current if Call is missing.
	loc := fr.Call
	if loc.Fn == nil && loc.File == "" && loc.Line == 0 {
		loc = fr.Current
	}

	fn := "<unknown-func>"
	if loc.Fn != nil && loc.Fn.Name != "" {
		fn = loc.Fn.Name
	}

	if loc.File == "" {
		if loc.PC != 0 {
			return fmt.Sprintf("%s (pc=0x%x)", fn, loc.PC)
		}
		return fn
	}

	if loc.Line > 0 {
		return fmt.Sprintf("%s (%s:%d)", fn, loc.File, loc.Line)
	}
	return fmt.Sprintf("%s (%s)", fn, loc.File)
}

func pointerLocals(vars []*proc.Variable) []*proc.Variable {
	out := make([]*proc.Variable, 0, len(vars))
	for _, v := range vars {
		if v == nil {
			continue
		}

		if v.Kind == reflect.Ptr || v.Kind == reflect.UnsafePointer {
			out = append(out, v)
			continue
		}

		// Fallback (defensive): sometimes Kind can be odd for optimized code.
		ts := v.TypeString()
		if strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer" {
			out = append(out, v)
		}
	}
	return out
}

func printPointerVar(w io.Writer, v *proc.Variable) {
	name := v.Name
	if name == "" {
		name = "<unnamed>"
	}

	typ := v.TypeString()
	if typ == "" {
		typ = "<unknown-type>"
	}

	// Where the local slot lives (often useful even when the value is unreadable).
	line := fmt.Sprintf("     * %s %s", name, typ)
	if v.Addr != 0 {
		line += fmt.Sprintf(" storage=0x%x", v.Addr)
	}

	// Pointer value (best effort).
	if ptr, ok := pointerValue(v); ok {
		line += fmt.Sprintf(" -> 0x%x", ptr)
	}

	// Keep Delve's own constant description when present.
	if s := strings.TrimSpace(v.ConstDescr()); s != "" {
		line += fmt.Sprintf(" value=%s", s)
	}

	if v.OnlyAddr {
		line += " (only-addr)"
	}
	if v.Unreadable != nil {
		line += fmt.Sprintf(" <unreadable: %v>", v.Unreadable)
	}

	fmt.Fprintln(w, line)
}

func pointerValue(v *proc.Variable) (uint64, bool) {
	// For pointers, Delve often populates Base with the pointed-to address.
	// But Value can also hold the raw pointer value as an integer constant.
	if v.Value != nil {
		if u, ok := constant.Uint64Val(v.Value); ok {
			return u, true
		}
		if i, ok := constant.Int64Val(v.Value); ok && i >= 0 {
			return uint64(i), true
		}
	}

	if v.Base != 0 {
		return v.Base, true
	}

	// If FollowPointers loaded the pointee child, its Addr is usually the target.
	if len(v.Children) > 0 && v.Children[0].Addr != 0 {
		return v.Children[0].Addr, true
	}

	return 0, false
}
