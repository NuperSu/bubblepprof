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
	exePath    string
	corePath   string
	depth      int // stack depth per goroutine
	pageSize   int // goroutine page size for debugger.Goroutines(start,count)
	objDepth   int // max object-walk depth (pointer hops / nesting)
	maxObjects int // safety cap per frame
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
	flag.IntVar(&o.objDepth, "objdepth", 8, "max depth while walking the object graph from stack locals (pointer hops / nesting)")
	flag.IntVar(&o.maxObjects, "maxobjects", 2000, "safety cap: max distinct objects to enter per frame while walking pointers")

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
	if o.objDepth <= 0 {
		o.objDepth = 1
	}
	if o.maxObjects <= 0 {
		o.maxObjects = 2000
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

	// NOTE: This controls how much Delve preloads into proc.Variable.Children.
	// We still do our own "visit each object only once" logic while *walking*.
	recurse := o.objDepth
	if recurse < 2 {
		recurse = 2 // we need at least one pointee level to see struct fields behind pointers
	}

	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: recurse,
		MaxStringLen:       256,
		MaxArrayValues:     64,
		MaxStructFields:    128,
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

			w := newObjWalker(out, o.objDepth, o.maxObjects)

			// Start the traversal from *all* locals on the frame stack.
			// - pointer-typed locals are roots
			// - struct locals can also contain pointer fields (even if the local itself isn't a pointer)
			entered := w.walkLocals(locals)
			if !entered {
				fmt.Fprintf(out, "     (no reachable pointers from locals)\n")
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

func isPointerLike(v *proc.Variable) bool {
	if v == nil {
		return false
	}
	if v.Kind == reflect.Ptr || v.Kind == reflect.UnsafePointer {
		return true
	}
	// Fallback (defensive): sometimes Kind can be odd for optimized code.
	ts := v.TypeString()
	return strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer"
}

func safeName(v *proc.Variable) string {
	if v == nil || v.Name == "" {
		return "<unnamed>"
	}
	return v.Name
}

func safeType(v *proc.Variable) string {
	if v == nil {
		return "<unknown-type>"
	}
	if ts := v.TypeString(); ts != "" {
		return ts
	}
	return "<unknown-type>"
}

// --------------------
// Object graph walker
// --------------------

type objWalker struct {
	out        io.Writer
	visited    map[uint64]struct{} // object address -> visited
	maxDepth   int                 // max recursion depth (pointer hops / nesting)
	maxObjects int                 // safety cap on distinct objects per frame
	objects    int                 // entered objects so far
}

func newObjWalker(out io.Writer, maxDepth, maxObjects int) *objWalker {
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if maxObjects <= 0 {
		maxObjects = 2000
	}
	return &objWalker{
		out:        out,
		visited:    make(map[uint64]struct{}, 128),
		maxDepth:   maxDepth,
		maxObjects: maxObjects,
	}
}

// walkLocals starts from all locals, but prints only the ones that can reach pointers.
// Returns true if we entered at least one distinct object.
func (w *objWalker) walkLocals(locals []*proc.Variable) bool {
	enteredAny := false

	for _, v := range locals {
		if v == nil {
			continue
		}
		if !w.containsPointers(v, w.maxDepth) {
			continue
		}

		name := safeName(v)
		typ := safeType(v)

		fmt.Fprintf(w.out, "     root %s %s\n", name, typ)
		w.walkValue(v, 0, name)

		if w.objects > 0 {
			enteredAny = true
		}
		if w.objects >= w.maxObjects {
			fmt.Fprintf(w.out, "     (stopped: reached maxobjects=%d)\n", w.maxObjects)
			return enteredAny
		}
	}

	return enteredAny
}

func (w *objWalker) containsPointers(v *proc.Variable, depth int) bool {
	if v == nil || depth < 0 {
		return false
	}
	if isPointerLike(v) {
		return true
	}

	switch v.Kind {
	case reflect.Struct, reflect.Array, reflect.Slice, reflect.Interface, reflect.Map:
		for i := range v.Children {
			c := &v.Children[i]
			// Skip common slice header pseudo-fields.
			if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
				continue
			}
			if w.containsPointers(c, depth-1) {
				return true
			}
		}
	}
	return false
}

func (w *objWalker) walkValue(v *proc.Variable, depth int, path string) {
	if v == nil {
		return
	}
	if depth > w.maxDepth {
		w.printf(depth, "%s ... (max depth reached)\n", path)
		return
	}

	if isPointerLike(v) {
		w.walkPointer(v, depth, path)
		return
	}

	switch v.Kind {
	case reflect.Struct:
		// Treat addressable struct locals as objects so &x and x are deduped.
		if v.Addr != 0 {
			w.enterObject(v.Addr, v, depth, path, true)
			return
		}
		// If we don't have an address, just scan inline.
		w.scanChildren(v, depth, path)
	case reflect.Array, reflect.Slice, reflect.Interface, reflect.Map:
		w.scanChildren(v, depth, path)
	default:
		// primitives: nothing to do
	}
}

func (w *objWalker) scanChildren(v *proc.Variable, depth int, path string) {
	for i := range v.Children {
		c := &v.Children[i]
		if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
			continue
		}

		childName := c.Name
		if childName == "" {
			childName = fmt.Sprintf("#%d", i)
		}

		nextPath := path
		switch v.Kind {
		case reflect.Array, reflect.Slice:
			nextPath = fmt.Sprintf("%s[%s]", path, childName)
		default:
			nextPath = path + "." + childName
		}

		w.walkValue(c, depth+1, nextPath)

		if w.objects >= w.maxObjects {
			return
		}
	}
}

func (w *objWalker) walkPointer(p *proc.Variable, depth int, path string) {
	ptr, ok := pointerValue(p)
	if !ok {
		w.printf(depth, "%s %s -> <unknown>\n", path, safeType(p))
		return
	}
	if ptr == 0 {
		w.printf(depth, "%s %s -> nil\n", path, safeType(p))
		return
	}

	// Always print the edge; only "enter" the target object once.
	if _, seen := w.visited[ptr]; seen {
		w.printf(depth, "%s %s -> 0x%x (already visited)\n", path, safeType(p), ptr)
		return
	}
	w.printf(depth, "%s %s -> 0x%x\n", path, safeType(p), ptr)

	if w.objects >= w.maxObjects {
		return
	}

	// We "enter" (dereference) the target object now.
	var pointee *proc.Variable
	if len(p.Children) > 0 {
		pointee = &p.Children[0]
	}
	if pointee == nil {
		w.printf(depth+1, "0x%x <no pointee loaded>\n", ptr)
		return
	}

	w.enterObject(ptr, pointee, depth+1, path, false)
}

func (w *objWalker) enterObject(addr uint64, v *proc.Variable, depth int, fromPath string, isStackLocal bool) {
	if addr == 0 {
		return
	}
	if _, seen := w.visited[addr]; seen {
		return
	}
	if w.objects >= w.maxObjects {
		return
	}

	w.visited[addr] = struct{}{}
	w.objects++

	kind := "object"
	if isStackLocal {
		kind = "stack-object"
	}

	w.printf(depth, "%s 0x%x %s\n", kind, addr, safeType(v))

	// Now scan *inside* the object and follow pointer fields.
	w.scanChildren(v, depth, fromPath)
}

func (w *objWalker) printf(depth int, format string, args ...any) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(w.out, indent+format, args...)
}
