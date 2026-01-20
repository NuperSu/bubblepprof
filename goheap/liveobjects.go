package goheap

import (
	"go/constant"
	"iter"
	"reflect"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/debugger"
)

// LiveObjects builds an in-memory graph of live objects reachable from a set of roots
// (typically stack locals, but can be any proc.Variable).
//
// Design goals:
//   - No recursion depth limits in the walker (it visits until it reaches a fixpoint).
//   - Linear time/space in the number of discovered objects/edges (no path copies).
//   - Traversal is side-effect free: it never prints.
//
// NOTE: The completeness of the traversal is constrained by the amount of debug
// information Delve loads into proc.Variable.Children (controlled by proc.LoadConfig).
type LiveObjects struct {
	// Delve debugger handle (used as a context / for future extension).
	dbg *debugger.Debugger

	visited map[uintptr]*LiveObject
}

type LiveObject struct {
	Addr uintptr

	// Var holds Delve's debug variable representing the object (type/kind/children).
	Var *proc.Variable

	// Children are outgoing edges to other objects (pointer-typed fields, array/slice
	// elements, map buckets/entries, chan internals, etc., depending on what Delve
	// loaded for Var.Children).
	Children []*LiveObject

	// internal: whether we already expanded Var.Children.
	scanned bool
}

func New(dbg *debugger.Debugger) *LiveObjects {
	return &LiveObjects{
		dbg:     dbg,
		visited: make(map[uintptr]*LiveObject, 4096),
	}
}

// Add adds a root value and walks all objects reachable from it (subject to what
// Delve loaded into proc.Variable.Children).
func (o *LiveObjects) Add(v *proc.Variable) {
	if v == nil {
		return
	}

	// Explicit worklist to avoid recursion and to keep traversal linear.
	type workItem struct {
		parent *LiveObject
		v      *proc.Variable
	}

	stack := make([]workItem, 0, 256)
	stack = append(stack, workItem{parent: nil, v: v})

	for len(stack) > 0 {
		// pop
		wi := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if wi.v == nil {
			continue
		}

		// Pointer-like values represent references to heap objects.
		if isPointerLike(wi.v) {
			addr, ok := pointerValue(wi.v)
			if !ok || addr == 0 {
				continue
			}

			childObj := o.enterObject(addr, pointeeOrSelf(wi.v))
			if childObj == nil {
				continue
			}
			if wi.parent != nil {
				wi.parent.Children = append(wi.parent.Children, childObj)
			}

			if childObj.scanned {
				continue
			}
			childObj.scanned = true

			// Expand children of the referenced object.
			for i := range childObj.Var.Children {
				c := &childObj.Var.Children[i]
				if isSliceLenCap(childObj.Var, c) {
					continue
				}
				stack = append(stack, workItem{parent: childObj, v: c})
			}
			continue
		}

		// Addressable structs are also treated as nodes to dedupe linked-list style graphs.
		if wi.v.Kind == reflect.Struct && wi.v.Addr != 0 {
			addr := uintptr(wi.v.Addr)
			obj := o.enterObject(addr, wi.v)
			if obj == nil {
				continue
			}
			if wi.parent != nil {
				wi.parent.Children = append(wi.parent.Children, obj)
			}

			if obj.scanned {
				continue
			}
			obj.scanned = true

			for i := range wi.v.Children {
				c := &wi.v.Children[i]
				if isSliceLenCap(wi.v, c) {
					continue
				}
				stack = append(stack, workItem{parent: obj, v: c})
			}
			continue
		}

		// Inline composites: traverse their children without creating a node.
		switch wi.v.Kind {
		case reflect.Struct, reflect.Array, reflect.Slice, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func:
			for i := range wi.v.Children {
				c := &wi.v.Children[i]
				if isSliceLenCap(wi.v, c) {
					continue
				}
				stack = append(stack, workItem{parent: wi.parent, v: c})
			}
		default:
			// primitives: nothing to do
		}
	}
}

// All returns the visited object set.
func (o *LiveObjects) All() iter.Seq2[uintptr, *LiveObject] {
	return func(yield func(uintptr, *LiveObject) bool) {
		for addr, obj := range o.visited {
			if !yield(addr, obj) {
				return
			}
		}
	}
}

func (o *LiveObjects) enterObject(addr uintptr, v *proc.Variable) *LiveObject {
	if addr == 0 || v == nil {
		return nil
	}
	if existing := o.visited[addr]; existing != nil {
		return existing
	}

	obj := &LiveObject{Addr: addr, Var: v}
	o.visited[addr] = obj
	return obj
}

// ---- helpers ----

func isPointerLike(v *proc.Variable) bool {
	if v == nil {
		return false
	}

	// In the Delve API, several Go kinds are represented as "values" that are in
	// practice pointers to runtime objects.
	switch v.Kind {
	case reflect.Ptr, reflect.UnsafePointer, reflect.Map, reflect.Chan, reflect.Func:
		return true
	}

	// Fallback: TypeString often carries the leading '*' for pointers.
	ts := v.TypeString()
	return strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer"
}

func isSliceLenCap(parent, child *proc.Variable) bool {
	return parent != nil && parent.Kind == reflect.Slice && child != nil && (child.Name == "len" || child.Name == "cap")
}

// Best-effort pointer extraction:
// - v.Value can hold the raw pointer value as an integer constant
// - v.Base sometimes holds it
func pointerValue(v *proc.Variable) (uintptr, bool) {
	if v == nil {
		return 0, false
	}

	max := ^uintptr(0)
	if v.Value != nil {
		if u, ok := constant.Uint64Val(v.Value); ok {
			if u <= uint64(max) {
				return uintptr(u), true
			}
			return 0, false
		}
		if i, ok := constant.Int64Val(v.Value); ok {
			if i >= 0 && uint64(i) <= uint64(max) {
				return uintptr(i), true
			}
			return 0, false
		}
	}

	if v.Base != 0 {
		if v.Base <= uint64(max) {
			return uintptr(v.Base), true
		}
		return 0, false
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
