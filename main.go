package main

import (
	"flag"
	"fmt"
	"go/constant"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"
)

func main() {
	var (
		exePath  = flag.String("exe", "", "path to the executable that produced the core (must match)")
		corePath = flag.String("core", "", "path to the core dump")
		depth    = flag.Int("depth", 64, "stack depth per goroutine")
		pageSize = flag.Int("page", 256, "goroutine page size for debugger.Goroutines(start,count)")
	)
	flag.Parse()

	if *exePath == "" || *corePath == "" {
		fmt.Fprintf(os.Stderr, "usage: %s -exe /path/to/bin -core /path/to/core [flags]\n", os.Args[0])
		os.Exit(2)
	}

	cfg := &debugger.Config{
		CoreFile:       *corePath,
		Backend:        "default",
		CheckGoVersion: true,
		ExecuteKind:    debugger.ExecutingExistingFile,
	}

	d, err := debugger.New(cfg, []string{*exePath})
	if err != nil {
		fmt.Fprintf(os.Stderr, "debugger.New: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = d.Detach(false) }()

	gs, err := allGoroutines(d, *pageSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list goroutines: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("goroutines: %d\n", len(gs))

	// IMPORTANT: make Delve actually load pointer targets (children) when possible.
	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 1,
		MaxStringLen:       256,
		MaxArrayValues:     64,
		MaxStructFields:    64,
	}

	for _, g := range gs {
		goid, ok := getInt64Field(g, "ID", "Goid", "GoID")
		if !ok {
			fmt.Printf("\n== goroutine: <unknown id> (%T)\n", g)
			continue
		}

		status, _ := getStringField(g, "Status")
		if status != "" {
			fmt.Printf("\n== goroutine %d (%s)\n", goid, status)
		} else {
			fmt.Printf("\n== goroutine %d\n", goid)
		}

		frames, err := d.Stacktrace(goid, *depth, api.StacktraceOptions(0))
		if err != nil {
			fmt.Printf("  stacktrace error: %v\n", err)
			continue
		}

		for fi, sf := range frames {
			fmt.Printf("  -- frame %d: %s\n", fi, describeStackframe(sf))

			locals, err := d.LocalVariables(goid, fi, 0, loadCfg)
			if err != nil {
				fmt.Printf("     locals error: %v\n", err)
				continue
			}

			ptrs := filterPointerVars(locals)
			if len(ptrs) == 0 {
				fmt.Printf("     (no pointer-typed locals)\n")
				continue
			}

			for _, v := range ptrs {
				name := v.Name
				if name == "" {
					name = "<unnamed>"
				}

				typ := v.TypeString()
				if typ == "" {
					typ = "<unknown-type>"
				}

				unreadable := getUnreadable(v)

				// storage address (where the local variable slot lives)
				storage := v.Addr

				// pointer target address (what the pointer points to)
				target, targetOK := pointerTargetAddr(v)

				// Delve sometimes has a printable Value string (often "0x..." for pointers)
				valStr := strings.TrimSpace(constValueString(v.Value))

				// Build a stable, useful line:
				//   name type storage=0x... -> 0x... value=...
				line := fmt.Sprintf("     * %s %s", name, typ)
				if storage != 0 {
					line += fmt.Sprintf(" storage=0x%x", storage)
				}
				if targetOK {
					line += fmt.Sprintf(" -> 0x%x", target)
				}
				if valStr != "" {
					line += fmt.Sprintf(" value=%s", valStr)
				}
				if unreadable != "" {
					line += fmt.Sprintf(" <unreadable: %s>", unreadable)
				}
				fmt.Println(line)
			}
		}
	}
}

// allGoroutines retrieves every goroutine using Debugger.Goroutines(start,count).
func allGoroutines(d *debugger.Debugger, pageSize int) ([]*proc.G, error) {
	if pageSize <= 0 {
		pageSize = 256
	}

	var out []*proc.G
	start := 0
	for {
		gs, total, err := d.Goroutines(start, pageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)

		start += len(gs)
		if start >= total || len(gs) == 0 {
			return out, nil
		}
	}
}

// filterPointerVars keeps only pointer-typed locals.
// Prefer v.Kind (stable) + a TypeString fallback.
func filterPointerVars(vars []*proc.Variable) []*proc.Variable {
	var out []*proc.Variable
	for _, v := range vars {
		if v == nil {
			continue
		}

		if v.Kind == reflect.Ptr || v.Kind == reflect.UnsafePointer {
			out = append(out, v)
			continue
		}

		// Fallback: if Kind is missing/odd in some cases, use the type string.
		ts := v.TypeString()
		if strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer" {
			out = append(out, v)
		}
	}
	return out
}

// pointerTargetAddr returns the address the pointer points to (best-effort).
//
// Order:
//  1. v.Base (often set to the pointer value for pointers)
//  2. v.Children[0].Addr (when FollowPointers loaded the pointee child)
//  3. parse v.Value if it looks like "0x...."
func pointerTargetAddr(v *proc.Variable) (uint64, bool) {
	if v == nil {
		return 0, false
	}

	if v.Base != 0 {
		return v.Base, true
	}

	if len(v.Children) > 0 && v.Children[0].Addr != 0 {
		return v.Children[0].Addr, true
	}

	if s := constValueString(v.Value); s != "" {
		if u, ok := parseAddrFromString(s); ok {
			return u, true
		}
	}

	return 0, false
}

func constValueString(cv constant.Value) string {
	if cv == nil {
		return ""
	}
	// constant.Value has String() returning a Go-syntax representation.
	// For pointers, Delve often stores an integer constant.
	return cv.String()
}

func parseAddrFromString(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}

	// Prefer 0x... anywhere in the string.
	if idx := strings.Index(s, "0x"); idx >= 0 {
		hexPart := s[idx+2:]
		end := 0
		for end < len(hexPart) {
			c := hexPart[end]
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
				end++
				continue
			}
			break
		}
		if end == 0 {
			return 0, false
		}
		u, err := strconv.ParseUint(hexPart[:end], 16, 64)
		return u, err == nil
	}

	// Otherwise, try to extract a decimal number token (common if Delve prints ints as "1234").
	// Find first digit.
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	u, err := strconv.ParseUint(s[start:end], 10, 64)
	return u, err == nil
}

/*
	func parseHexAddr(s string) (uint64, bool) {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, false
		}
		// Common cases:
		//   "0x1234"
		//   "(unsafe.Pointer)(0x1234)"
		//   "*main.T 0xc000...." (less common but happens)
		idx := strings.Index(s, "0x")
		if idx < 0 {
			return 0, false
		}
		hexPart := s[idx+2:]
		// cut at first non-hex
		end := 0
		for end < len(hexPart) {
			c := hexPart[end]
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
				end++
				continue
			}
			break
		}
		if end == 0 {
			return 0, false
		}
		u, err := strconv.ParseUint(hexPart[:end], 16, 64)
		return u, err == nil
	}
*/
func describeStackframe(sf proc.Stackframe) string {
	if s := tryDescribeFromLocation(sf, "Call"); s != "" {
		return s
	}
	if s := tryDescribeFromLocation(sf, "Current"); s != "" {
		return s
	}
	if pc, ok := getUint64Field(&sf, "PC"); ok && pc != 0 {
		return fmt.Sprintf("pc=0x%x (%+v)", pc, sf)
	}
	return fmt.Sprintf("%+v", sf)
}

func tryDescribeFromLocation(sf proc.Stackframe, locField string) string {
	rv := reflect.ValueOf(sf)
	f := rv.FieldByName(locField)
	if !f.IsValid() {
		return ""
	}

	file := ""
	line := int64(0)
	if s, ok := getStringFromValue(f, "File"); ok {
		file = s
	}
	if n, ok := getInt64FromValue(f, "Line"); ok {
		line = n
	}

	fnName := ""
	if fn, ok := fieldByNameDeep(f, "Fn"); ok && fn.IsValid() {
		if s, ok := getStringFromValue(fn, "Name"); ok {
			fnName = s
		}
	}
	if fnName == "" {
		if s, ok := getStringFromValue(f, "Function"); ok {
			fnName = s
		}
	}

	if fnName == "" && file == "" && line == 0 {
		return ""
	}

	if fnName == "" {
		fnName = "<unknown-func>"
	}
	if file == "" {
		return fnName
	}
	if line > 0 {
		return fmt.Sprintf("%s (%s:%d)", fnName, file, line)
	}
	return fmt.Sprintf("%s (%s)", fnName, file)
}

// ---- reflection helpers (your originals) ----

func getUnreadable(v any) string {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if !rv.IsValid() {
		return ""
	}
	f := rv.FieldByName("Unreadable")
	if !f.IsValid() || f.IsZero() {
		return ""
	}
	if f.Kind() == reflect.String {
		return f.String()
	}
	if f.CanInterface() {
		if err, ok := f.Interface().(error); ok && err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%v", f.Interface())
	}
	return ""
}

func getStringField(v any, names ...string) (string, bool) {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if !rv.IsValid() {
		return "", false
	}
	for _, name := range names {
		f := rv.FieldByName(name)
		if f.IsValid() && f.Kind() == reflect.String {
			return f.String(), true
		}
	}
	return "", false
}

func getInt64Field(v any, names ...string) (int64, bool) {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if !rv.IsValid() {
		return 0, false
	}
	for _, name := range names {
		f := rv.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return f.Int(), true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return int64(f.Uint()), true
		}
	}
	return 0, false
}

func getUint64Field(v any, names ...string) (uint64, bool) {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if !rv.IsValid() {
		return 0, false
	}
	for _, name := range names {
		f := rv.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return f.Uint(), true
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n := f.Int()
			if n >= 0 {
				return uint64(n), true
			}
		}
	}
	return 0, false
}

func fieldByNameDeep(v reflect.Value, name string) (reflect.Value, bool) {
	v = reflect.Indirect(v)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	f := v.FieldByName(name)
	return f, f.IsValid()
}

func getStringFromValue(v reflect.Value, field string) (string, bool) {
	v = reflect.Indirect(v)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return "", false
	}
	f := v.FieldByName(field)
	if f.IsValid() && f.Kind() == reflect.String {
		return f.String(), true
	}
	return "", false
}

func getInt64FromValue(v reflect.Value, field string) (int64, bool) {
	v = reflect.Indirect(v)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return 0, false
	}
	f := v.FieldByName(field)
	if !f.IsValid() {
		return 0, false
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return f.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(f.Uint()), true
	default:
		return 0, false
	}
}
