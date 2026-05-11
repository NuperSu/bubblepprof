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
// Delve variables (typically locals from one goroutine's stack frames).
//
// Design: traversal is separated from output. The caller supplies a
// proc.LoadConfig with MaxVariableRecurse=1 so that each Delve variable
// fetch returns only immediate children. When the traversal encounters a
// variable whose children were not materialized (due to the recursion
// limit), it lazily reloads the variable via EvalVariableInScope using a
// synthetic unsafe.Pointer cast. This works for all composite types:
// typed pointers (*T), structs, slices, arrays, maps, channels, and
// interfaces. The visited map deduplicates nodes by address so each
// object is loaded at most once.
//
// Limitations:
//   - closure capture environments (func values are treated as opaque addresses)
//   - global/static roots (only goroutine stack locals are used as roots)
type LiveObjects struct {
	dbg     *debugger.Debugger
	loadCfg proc.LoadConfig

	visited map[uintptr]*LiveObject

	runtimeRootsAdded bool

	// Scope for EvalVariableInScope calls. Updated by each Add() call. Note:
	// this means all lazy loads use the scope of the most recent Add() call,
	// which is correct only because any valid goroutine scope can read
	// arbitrary memory addresses via unsafe.Pointer casts.
	scopeGID   int64
	scopeFrame int

	// Diagnostic counters for traversal analysis.
	Stats TraversalStats
}

// TraversalStats tracks diagnostic counters for the graph traversal.
type TraversalStats struct {
	StackPops          int // total items popped from the worklist
	LoadAtCalls        int // total EvalVariableInScope calls via loadAt
	LoadPointeeCalls   int // loadAt calls from loadPointee (typed pointer targets)
	EnsureLoadedCalls  int // loadAt calls from ensureLoaded (composites at recursion boundary)
	PointerReloadCalls int // loadAt calls from OnlyAddr pointer reload
	DedupHits          int // pointers skipped because target was already visited
	WastedLoads        int // loadPointee calls where address was already visited
	EnsureByKind       map[string]int // ensureLoaded calls broken down by Kind+Type
}

type LiveObject struct {
	Addr uintptr

	// Delve's view of this address: type, kind, value, and any materialized
	// children. May represent a heap allocation, a stack-local, or a runtime
	// structure, but no heap/stack distinction is tracked here.
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

// walkFromRoots traverses discovered Delve variables using an explicit stack
// (LIFO). For each variable it ensures children are loaded (reloading via
// ensureLoaded if the recursion limit left them empty), enters addressable
// objects into the visited map for deduplication, and pushes children for
// further processing. Pointer-like types (Ptr, Map, Chan, Func) create
// nodes at the target/runtime address; addressable structs create nodes at
// their own address. The traversal terminates when all reachable nodes are
// visited. It is bounded by Delve's loadCfg limits (MaxArrayValues,
// MaxStructFields) which cap how many children a single variable exposes.
func (o *LiveObjects) walkFromRoots(roots []root) {
	stack := make([]root, 0, len(roots)+64)
	stack = append(stack, roots...)

	for len(stack) > 0 {
		n := len(stack) - 1
		cur := stack[n]
		stack = stack[:n]
		o.Stats.StackPops++

		v := cur.v
		if v == nil {
			continue
		}

		// Pointer-like values (Ptr, Map, Chan, Func, UnsafePointer) identify
		// a target address. The node in the graph is keyed by that address.
		if isPointerLike(v) {
			ptr, ok := pointerValue(v)

			// Variable might not be fully loaded (OnlyAddr=true from
			// recursion limit on pointer-to-pointer chains). Reload
			// from its address so we can extract the pointer value.
			if (!ok || ptr == 0) && v.Addr != 0 {
				if reloaded := o.loadAt(v.TypeString(), uintptr(v.Addr)); reloaded != nil {
					o.Stats.PointerReloadCalls++
					v = reloaded
					ptr, ok = pointerValue(v)
				}
			}

			if !ok || ptr == 0 {
				continue
			}

			// Fast path: if already visited, just record the edge.
			if existing := o.visited[ptr]; existing != nil {
				if cur.parent != nil {
					cur.parent.Children = append(cur.parent.Children, existing)
				}
				o.Stats.DedupHits++
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
			childObj.scanned = true

			// Reload the variable if its children were truncated by the
			// recursion limit. Works for structs, maps, slices, etc.
			expanded := o.ensureLoaded(childObj.Var)
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

				expanded := o.ensureLoaded(v)
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
			v = o.ensureLoaded(v)
			pushChildren(&stack, cur.parent, v)
		default:
			// primitives: nothing to do
		}
	}
}

// addRuntimeRoots attempts to add runtime finalizer queues (runtime.allfin,
// runtime.finq) as extra graph roots. These are internal runtime symbols whose
// names and layouts change between Go versions. The function evaluates them via
// EvalVariableInScope and walks whatever Delve materializes; it does not parse
// the finalizer linked-list structure itself. Called once per LiveObjects
// instance on the first Add().
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
	result := o.loadAt(baseType, addr)
	if result != nil {
		o.Stats.LoadPointeeCalls++
	}
	return result
}

// ensureLoaded reloads addressable variables whose children were not present
// in the initial Delve variable fetch (typically due to MaxVariableRecurse
// limits). Handles all composite types: structs, slices, arrays, maps,
// channels, and interfaces.
func (o *LiveObjects) ensureLoaded(v *proc.Variable) *proc.Variable {
	if v == nil || len(v.Children) > 0 || v.Addr == 0 {
		return v
	}
	switch v.Kind {
	case reflect.Struct, reflect.Slice, reflect.Array,
		reflect.Map, reflect.Chan, reflect.Interface:
		if loaded := o.loadAt(v.TypeString(), uintptr(v.Addr)); loaded != nil {
			o.Stats.EnsureLoadedCalls++
			return loaded
		}
	}
	return v
}

// loadAt evaluates a synthetic unsafe.Pointer expression so Delve reads memory
// at addr as typeName. It returns nil when the expression cannot be evaluated.
func (o *LiveObjects) loadAt(typeName string, addr uintptr) *proc.Variable {
	o.Stats.LoadAtCalls++
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
