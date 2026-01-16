package main

import (
	"go/constant"
	"iter"
	"reflect"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/debugger"
)

type LiveObjects struct {
	// pointer(s) to Delve service(s)
	dbg *debugger.Debugger

	visited map[uintptr]*LiveObject

	maxDepth   int
	maxObjects int
}

type LiveObject struct {
	Addr uintptr

	// debug info pointer for this object (keep the variable for type/kind/children)
	Var *proc.Variable

	// pointer-typed fields of a struct, or array elements
	Children []*LiveObject

	// internal: to avoid rescanning already-expanded nodes
	scanned bool
}

func NewLiveObjects(dbg *debugger.Debugger, maxDepth, maxObjects int) *LiveObjects {
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if maxObjects <= 0 {
		maxObjects = 2000
	}
	return &LiveObjects{
		dbg:        dbg,
		visited:    make(map[uintptr]*LiveObject, 256),
		maxDepth:   maxDepth,
		maxObjects: maxObjects,
	}
}

// Add adds a “root” value (typically a local on a goroutine stack frame).
// Traversal populates visited + edges; it does not print.
func (o *LiveObjects) Add(v *proc.Variable) {
	if v == nil {
		return
	}
	if !o.containsPointers(v, o.maxDepth) {
		return
	}
	o.walkValue(nil, v, 0)
}

func (o *LiveObjects) All() iter.Seq2[uintptr, *LiveObject] {
	return func(yield func(uintptr, *LiveObject) bool) {
		for addr, obj := range o.visited {
			if !yield(addr, obj) {
				return
			}
		}
	}
}

// ---- traversal internals (no printing) ----

func (o *LiveObjects) containsPointers(v *proc.Variable, depth int) bool {
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
			if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
				continue
			}
			if o.containsPointers(c, depth-1) {
				return true
			}
		}
	}
	return false
}

// parent may be nil for roots (locals).
func (o *LiveObjects) walkValue(parent *LiveObject, v *proc.Variable, depth int) {
	if v == nil || depth > o.maxDepth {
		return
	}
	if len(o.visited) >= o.maxObjects {
		return
	}

	if isPointerLike(v) {
		o.walkPointer(parent, v, depth)
		return
	}

	switch v.Kind {
	case reflect.Struct:
		// If addressable, treat it as a node too (dedupe by address).
		if v.Addr != 0 {
			obj := o.enterObject(uintptr(v.Addr), v)
			if obj == nil {
				return
			}
			if parent != nil {
				parent.Children = append(parent.Children, obj)
			}
			o.scanChildren(obj, v, depth)
			return
		}
		// Not addressable: scan inline.
		o.scanChildren(parent, v, depth)

	case reflect.Array, reflect.Slice, reflect.Interface, reflect.Map:
		o.scanChildren(parent, v, depth)

	default:
		// primitives: nothing to do
	}
}

func (o *LiveObjects) scanChildren(parent *LiveObject, v *proc.Variable, depth int) {
	for i := range v.Children {
		if len(o.visited) >= o.maxObjects {
			return
		}

		c := &v.Children[i]
		if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
			continue
		}
		o.walkValue(parent, c, depth+1)
	}
}

func (o *LiveObjects) walkPointer(parent *LiveObject, p *proc.Variable, depth int) {
	ptr, ok := pointerValue(p)
	if !ok || ptr == 0 {
		return
	}

	addr := uintptr(ptr)
	childObj := o.enterObject(addr, pointeeOrSelf(p))
	if childObj == nil {
		return
	}

	if parent != nil {
		parent.Children = append(parent.Children, childObj)
	}

	// If already expanded, stop here.
	if childObj.scanned {
		return
	}

	// Expand inside pointee and mark scanned.
	o.scanChildren(childObj, childObj.Var, depth+1)
	childObj.scanned = true
}

func (o *LiveObjects) enterObject(addr uintptr, v *proc.Variable) *LiveObject {
	if addr == 0 || v == nil {
		return nil
	}
	if existing := o.visited[addr]; existing != nil {
		return existing
	}
	if len(o.visited) >= o.maxObjects {
		return nil
	}

	obj := &LiveObject{
		Addr:    addr,
		Var:     v,
		scanned: false,
	}
	o.visited[addr] = obj
	return obj
}

// ---- helpers ----

func isPointerLike(v *proc.Variable) bool {
	if v == nil {
		return false
	}
	if v.Kind == reflect.Ptr || v.Kind == reflect.UnsafePointer {
		return true
	}
	ts := v.TypeString()
	return strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer"
}

// Best-effort pointer extraction:
// - v.Value often holds the pointer numeric value
// - v.Base sometimes holds it
func pointerValue(v *proc.Variable) (uint64, bool) {
	if v == nil {
		return 0, false
	}

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

	return 0, false
}

// If FollowPointers is enabled, p.Children[0] is usually the dereferenced value.
func pointeeOrSelf(p *proc.Variable) *proc.Variable {
	if p == nil {
		return nil
	}
	if len(p.Children) > 0 {
		return &p.Children[0]
	}
	return p
}
