package goheap

import (
	"fmt"
	"go/constant"
	"iter"
	"reflect"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/debugger"
)

// LiveObjects builds a graph of addressable objects reachable from a set of
// Delve variables, usually locals from one goroutine stack.
//
// Traversal is separated from any printing or formatting.
//
// The caller supplies a proc.LoadConfig with a small MaxVariableRecurse (e.g. 1).
// When the traversal hits a pointer whose pointee was not materialized by Delve,
// it lazily re-evaluates the pointee via EvalVariableInScope. The visited map
// deduplicates graph nodes by address.
//
// Current scope: this follows ordinary typed pointers and scans the children
// Delve exposes for structs, arrays, slices, interfaces, maps, and channels.
// It does not decode Go runtime map buckets, channel buffers, closure payloads,
// or pprof labels/bubbles by itself.
type LiveObjects struct {
	dbg     *debugger.Debugger
	loadCfg proc.LoadConfig

	visited map[uintptr]*LiveObject

	runtimeRootsAdded bool

	// Scope used when evaluating synthetic expressions for lazy loads. Add
	// updates it to the goroutine/frame that supplied the current root.
	scopeGID   int64
	scopeFrame int
}

type LiveObject struct {
	Addr uintptr

	// Delve view of this address, including type, kind, and any materialized
	// children. It may be a heap object, stack object, or runtime object.
	Var *proc.Variable

	// Reachability edges discovered by scanning Delve children.
	Children []*LiveObject

	// Internal traversal marker to avoid expanding the same address repeatedly.
	scanned bool
}

func New(dbg *debugger.Debugger, loadCfg proc.LoadConfig) *LiveObjects {
	return &LiveObjects{
		dbg:     dbg,
		loadCfg: loadCfg,
		visited: make(map[uintptr]*LiveObject, 256),
	}
}

// Add adds one root variable to the graph.
// gid and frame identify the Delve scope used for lazy memory reads. Traversal
// populates visited nodes and edges only; reporting is handled by callers.
func (o *LiveObjects) Add(v *proc.Variable, gid int64, frame int) {
	if v == nil {
		return
	}
	o.scopeGID = gid
	o.scopeFrame = frame
	o.addRuntimeRoots()
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

// walkFromRoots traverses discovered Delve variables with an explicit stack.
// The traversal has no object-depth limit of its own, but it is still bounded
// by what Delve can expose and by loadCfg limits such as MaxArrayValues and
// MaxStructFields.
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

		// Pointer-like values identify another address. For *T pointers this is
		// the pointed-to object; for maps/channels/functions it is only the
		// runtime object address unless Delve has already materialized children.
		if isPointerLike(v) {
			ptr, ok := pointerValue(v)
			if !ok || ptr == 0 {
				continue
			}

			pointee := pointeeOrSelf(v)

			// If a typed pointer was not materialized, load *T at ptr.
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

			// Give addressable structs one more chance to expose their fields.
			expanded := o.ensureExpanded(childObj.Var)
			if expanded != childObj.Var {
				childObj.Var = expanded
			}
			pushChildren(&stack, childObj, expanded)
			continue
		}

		switch v.Kind {
		case reflect.Struct:
			// Addressable structs become nodes even when reached inline.
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
			// Non-addressable values can still contain pointers; scan them
			// without creating a node for the value itself.
			pushChildren(&stack, cur.parent, v)

		case reflect.Array, reflect.Slice, reflect.Interface, reflect.Map, reflect.Chan:
			pushChildren(&stack, cur.parent, v)
		default:
			// primitives: nothing to do
		}
	}
}

// addRuntimeRoots best-effort adds runtime finalizer queues as extra roots.
// This is a heuristic: symbol names and layouts are runtime-version-dependent,
// and this does not replace proper decoding of runtime finalizer structures.
func (o *LiveObjects) addRuntimeRoots() {
	if o.runtimeRootsAdded {
		return
	}
	o.runtimeRootsAdded = true

	exprs := []string{
		"runtime.allfin",
		"runtime.finq",
	}
	roots := make([]root, 0, len(exprs))
	for _, expr := range exprs {
		v, err := o.dbg.EvalVariableInScope(o.scopeGID, o.scopeFrame, 0, expr, o.loadCfg)
		if err != nil || v == nil {
			continue
		}
		roots = append(roots, root{v: v})
	}
	if len(roots) == 0 {
		return
	}
	o.walkFromRoots(roots)
}

// loadPointee loads the *T target of a typed pointer using EvalVariableInScope.
// It deliberately does not decode runtime internals behind chan/map/func or
// unsafe.Pointer values.
func (o *LiveObjects) loadPointee(ptrVar *proc.Variable, addr uintptr) *proc.Variable {
	ts := ptrVar.TypeString()
	if !strings.HasPrefix(ts, "*") {
		return nil
	}
	baseType := ts[1:] // e.g. "*main.Node" becomes "main.Node"
	return o.loadAt(baseType, addr)
}

// ensureExpanded reloads addressable structs whose children were not present in
// the first Delve variable fetch.
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

// loadAt evaluates a synthetic unsafe.Pointer expression so Delve reads memory
// at addr as typeName. It returns nil when the expression cannot be evaluated.
func (o *LiveObjects) loadAt(typeName string, addr uintptr) *proc.Variable {
	expr := fmt.Sprintf("*(*%s)(unsafe.Pointer(uintptr(%d)))", typeName, addr)
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
	// Push in reverse so Delve's child order is preserved by the LIFO stack.
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
		Addr: addr,
		Var:  v,
	}
	o.visited[addr] = obj
	return obj
}

// ---- helpers ----

func isPointerLike(v *proc.Variable) bool {
	if v == nil {
		return false
	}
	switch v.Kind {
	case reflect.Ptr, reflect.UnsafePointer,
		reflect.Chan, reflect.Map, reflect.Func:
		return true
	}

	ts := v.TypeString()
	return strings.HasPrefix(ts, "*") || ts == "unsafe.Pointer"
}

// pointerValue extracts the address carried by a Delve pointer-like variable.
func pointerValue(v *proc.Variable) (uintptr, bool) {
	if v == nil {
		return 0, false
	}

	// Prefer Value when Delve exposed the pointer as an integer constant.
	if v.Value != nil {
		if v.Value.Kind() == constant.Int {
			// Uint64Val and Int64Val are only meaningful for integer constants.
			if u, ok := constant.Uint64Val(v.Value); ok {
				return uintptr(u), true
			}
			if i, ok := constant.Int64Val(v.Value); ok && i >= 0 {
				return uintptr(i), true
			}
		}
		// Non-int constants (unknown/string/bool/etc.) -> ignore.
	}

	// Delve often stores the pointee or runtime object address here.
	if v.Base != 0 {
		return uintptr(v.Base), true
	}

	return 0, false
}

// pointeeOrSelf returns the dereferenced value if FollowPointers materialized it.
func pointeeOrSelf(p *proc.Variable) *proc.Variable {
	if p == nil {
		return nil
	}
	if isTypedPointer(p) && len(p.Children) > 0 {
		return &p.Children[0]
	}
	return p
}

func isTypedPointer(v *proc.Variable) bool {
	if v == nil {
		return false
	}
	if v.Kind == reflect.Ptr {
		return true
	}
	return strings.HasPrefix(v.TypeString(), "*")
}
