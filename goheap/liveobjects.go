package goheap

import (
	"fmt"
	"go/constant"
	"iter"
	"reflect"
	"strings"
	"unsafe"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/debugger"
)

// LiveObjects builds a graph of heap objects reachable from a set of roots
// (typically locals on goroutine stacks).
//
// Traversal is separated from any printing or formatting.
//
// The caller supplies a proc.LoadConfig with a small MaxVariableRecurse (e.g. 1).
// When the traversal hits a pointer whose pointee was not materialized by Delve,
// it lazily re-evaluates the pointee via EvalVariableInScope. The visited map
// deduplicates by address, so each object is loaded at most once.
type LiveObjects struct {
	dbg     *debugger.Debugger
	loadCfg proc.LoadConfig

	visited map[uintptr]*LiveObject

	// Scope for lazy EvalVariableInScope calls.
	// Updated by Add; any valid goroutine+frame works for memory reads.
	scopeGID   int64
	scopeFrame int
}

type LiveObject struct {
	Addr uintptr

	// debug info pointer for this object (keep the variable for type/kind/children)
	Var *proc.Variable

	// pointer-typed fields of a struct, or array/slice/map elements, etc.
	Children []*LiveObject

	// internal: to avoid rescanning already-expanded nodes
	scanned bool
}

func New(dbg *debugger.Debugger, loadCfg proc.LoadConfig) *LiveObjects {
	return &LiveObjects{
		dbg:     dbg,
		loadCfg: loadCfg,
		visited: make(map[uintptr]*LiveObject, 256),
	}
}

// Add adds a "root" value (typically a local on a goroutine stack frame).
// gid and frame identify a valid scope for lazy re-evaluation.
// Traversal populates visited + edges; it does not print.
func (o *LiveObjects) Add(v *proc.Variable, gid int64, frame int) {
	if v == nil {
		return
	}
	o.scopeGID = gid
	o.scopeFrame = frame
	o.walkFromRoots([]root{{parent: nil, v: v}})
}

// All returns all unique objects discovered so far.
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

type root struct {
	parent *LiveObject
	v      *proc.Variable
}

// walkFromRoots performs an unbounded traversal using an explicit stack.
// When a pointer's pointee was truncated by Delve's MaxVariableRecurse limit,
// it lazily loads the pointee via EvalVariableInScope.
func (o *LiveObjects) walkFromRoots(roots []root) {
	stack := make([]root, 0, len(roots)+64)
	stack = append(stack, roots...)

	for len(stack) > 0 {
		n := len(stack) - 1
		cur := stack[n]
		stack = stack[:n]

		v := cur.v
		if v == nil {
			continue
		}

		// Pointer-like values: create/lookup the pointee node and expand it once.
		if isPointerLike(v) {
			ptr, ok := pointerValue(v)
			if !ok || ptr == 0 {
				continue
			}

			pointee := pointeeOrSelf(v)

			// If the pointee was not materialized (truncated), lazy-load it.
			if pointee == v || len(pointee.Children) == 0 {
				if loaded := o.loadPointee(v, ptr); loaded != nil {
					pointee = loaded
				}
			}

			childObj := o.enterObject(ptr, pointee)
			if childObj == nil {
				continue
			}
			if cur.parent != nil {
				cur.parent.Children = append(cur.parent.Children, childObj)
			}

			if childObj.scanned {
				continue
			}
			childObj.scanned = true

			// Ensure the variable has children before pushing.
			expanded := o.ensureExpanded(childObj.Var)
			if expanded != childObj.Var {
				childObj.Var = expanded
			}
			pushChildren(&stack, childObj, expanded)
			continue
		}

		switch v.Kind {
		case reflect.Struct:
			// If addressable, treat it as a node too (dedupe by address).
			if v.Addr != 0 {
				obj := o.enterObject(uintptr(v.Addr), v)
				if obj == nil {
					continue
				}
				if cur.parent != nil {
					cur.parent.Children = append(cur.parent.Children, obj)
				}
				if obj.scanned {
					continue
				}
				obj.scanned = true

				expanded := o.ensureExpanded(v)
				if expanded != v {
					obj.Var = expanded
				}
				pushChildren(&stack, obj, expanded)
				continue
			}
			// Not addressable: scan inline.
			pushChildren(&stack, cur.parent, v)

		case reflect.Array, reflect.Slice, reflect.Interface, reflect.Map, reflect.Chan:
			pushChildren(&stack, cur.parent, v)
		default:
			// primitives: nothing to do
		}
	}
}

// loadPointee loads the object that ptrVar points to, using EvalVariableInScope.
// Only handles *T pointer types; returns nil for chan/map/func/unsafe.Pointer.
func (o *LiveObjects) loadPointee(ptrVar *proc.Variable, addr uintptr) *proc.Variable {
	ts := ptrVar.TypeString()
	if !strings.HasPrefix(ts, "*") {
		return nil
	}
	baseType := ts[1:] // e.g. "*main.Node" → "main.Node"
	return o.loadAt(baseType, addr)
}

// ensureExpanded reloads a variable from memory if its children were truncated.
func (o *LiveObjects) ensureExpanded(v *proc.Variable) *proc.Variable {
	if v == nil || len(v.Children) > 0 || v.Addr == 0 {
		return v
	}
	switch v.Kind {
	case reflect.Struct:
		if loaded := o.loadAt(v.TypeString(), uintptr(v.Addr)); loaded != nil {
			return loaded
		}
	}
	return v
}

// loadAt evaluates *(*typeName)(unsafe.Pointer(uintptr(addr))) to load a
// variable from the core dump. Returns nil on any error.
func (o *LiveObjects) loadAt(typeName string, addr uintptr) *proc.Variable {
	expr := fmt.Sprintf("*(*%s)(unsafe.Pointer(uintptr(%d)))", typeName, uint64(addr))
	v, err := o.dbg.EvalVariableInScope(o.scopeGID, o.scopeFrame, 0, expr, o.loadCfg)
	if err != nil {
		return nil
	}
	return v
}

func pushChildren(stack *[]root, parent *LiveObject, v *proc.Variable) {
	if v == nil {
		return
	}
	// Push in reverse so the natural order is preserved with a LIFO stack.
	for i := len(v.Children) - 1; i >= 0; i-- {
		c := &v.Children[i]
		if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
			continue
		}
		*stack = append(*stack, root{parent: parent, v: c})
	}
}

func (o *LiveObjects) enterObject(addr uintptr, v *proc.Variable) *LiveObject {
	if addr == 0 || v == nil {
		return nil
	}
	if existing := o.visited[addr]; existing != nil {
		return existing
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
	// Real pointer kinds.
	if v.Kind == reflect.Ptr || v.Kind == reflect.UnsafePointer {
		return true
	}
	// Chan/map/func values are runtime pointers too (hchan/hmap/funcval).
	if v.Kind == reflect.Chan || v.Kind == reflect.Map || v.Kind == reflect.Func {
		return true
	}

	ts := v.TypeString()
	if strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer" {
		return true
	}
	return false
}

// Best-effort pointer extraction.
//
// Note: proc.Variable.Value can hold the raw pointer value as an integer
// constant; if it overflows uintptr we ignore it.
func pointerValue(v *proc.Variable) (uintptr, bool) {
	if v == nil {
		return 0, false
	}

	if v.Value != nil {
		if u, ok := constant.Uint64Val(v.Value); ok {
			if u <= uint64(^uintptr(0)) {
				return uintptr(u), true
			}
			return 0, false
		}
		if i, ok := constant.Int64Val(v.Value); ok && i >= 0 {
			ui := uint64(i)
			if ui <= uint64(^uintptr(0)) {
				return uintptr(ui), true
			}
			return 0, false
		}
	}

	if v.Base != 0 {
		// v.Base is uint64 in Delve, but represents an address.
		if v.Base <= uint64(^uintptr(0)) {
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

// Make sure uintptr size matches our assumptions at build time.
var _ = unsafe.Sizeof(uintptr(0))
