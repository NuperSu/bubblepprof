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

// ObjectID identifies one process-wide object record.
type ObjectID uint32

// ProcessGraph stores each discovered object address once for the whole
// process. Delve variables are used only while scanning and are not retained in
// this graph.
type ProcessGraph struct {
	ByAddr  map[uintptr]ObjectID
	Objects []Object

	scanned map[ObjectID]bool
}

// Object is the compact persistent record for one addressable object.
type Object struct {
	ID       ObjectID
	Addr     uintptr
	Size     uint64
	TypeName string
	Children []ObjectID
}

// GoroutineInfo stores only compact references into the process graph.
type GoroutineInfo struct {
	ID        uint64
	Labels    map[string]string
	Roots     []ObjectID
	Reachable map[ObjectID]struct{}
}

// NewProcessGraph creates an empty process-wide object table.
func NewProcessGraph() *ProcessGraph {
	return &ProcessGraph{
		ByAddr:  make(map[uintptr]ObjectID, 1024),
		Objects: make([]Object, 0, 1024),
		scanned: make(map[ObjectID]bool, 1024),
	}
}

// Object returns the object for id, or nil for an invalid id.
func (g *ProcessGraph) Object(id ObjectID) *Object {
	if g == nil || int(id) < 0 || int(id) >= len(g.Objects) {
		return nil
	}
	return &g.Objects[id]
}

// All returns all process-wide objects discovered so far.
func (g *ProcessGraph) All() iter.Seq2[ObjectID, *Object] {
	return func(yield func(ObjectID, *Object) bool) {
		if g == nil {
			return
		}
		for i := range g.Objects {
			id := ObjectID(i)
			if !yield(id, &g.Objects[i]) {
				return
			}
		}
	}
}

func (g *ProcessGraph) enterObject(addr uintptr, v *proc.Variable) (ObjectID, bool) {
	if addr == 0 || v == nil {
		return 0, false
	}
	if id, ok := g.ByAddr[addr]; ok {
		obj := &g.Objects[id]
		if obj.TypeName == "" {
			obj.TypeName = typeName(v)
		}
		if obj.Size == 0 {
			obj.Size = objectSize(v)
		}
		return id, true
	}

	id := ObjectID(len(g.Objects))
	g.ByAddr[addr] = id
	g.Objects = append(g.Objects, Object{
		ID:       id,
		Addr:     addr,
		Size:     objectSize(v),
		TypeName: typeName(v),
	})
	return id, true
}

func (g *ProcessGraph) addChild(parent, child ObjectID) {
	if g == nil || int(parent) >= len(g.Objects) || int(child) >= len(g.Objects) {
		return
	}
	children := g.Objects[parent].Children
	for _, existing := range children {
		if existing == child {
			return
		}
	}
	g.Objects[parent].Children = append(children, child)
}

func (g *ProcessGraph) isScanned(id ObjectID) bool {
	return g != nil && g.scanned[id]
}

func (g *ProcessGraph) markScanned(id ObjectID) {
	if g != nil {
		g.scanned[id] = true
	}
}

// LiveObjects scans one goroutine's roots into a shared ProcessGraph. Its
// persistent per-goroutine state is only roots and reachable object IDs.
type LiveObjects struct {
	dbg     *debugger.Debugger
	loadCfg proc.LoadConfig
	graph   *ProcessGraph
	info    GoroutineInfo

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
	StackPops          int            // total items popped from the worklist
	LoadAtCalls        int            // total EvalVariableInScope calls via loadAt
	LoadPointeeCalls   int            // loadAt calls from loadPointee (typed pointer targets)
	EnsureLoadedCalls  int            // loadAt calls from ensureLoaded (composites at recursion boundary)
	PointerReloadCalls int            // loadAt calls from OnlyAddr pointer reload
	DedupHits          int            // pointers skipped because target was already globally scanned
	WastedLoads        int            // retained for report compatibility
	EnsureByKind       map[string]int // ensureLoaded calls broken down by Kind+Type
}

func New(graph *ProcessGraph, dbg *debugger.Debugger, loadCfg proc.LoadConfig, gid uint64, labels map[string]string) *LiveObjects {
	if graph == nil {
		graph = NewProcessGraph()
	}
	return &LiveObjects{
		dbg:     dbg,
		loadCfg: loadCfg,
		graph:   graph,
		info: GoroutineInfo{
			ID:        gid,
			Labels:    labels,
			Roots:     make([]ObjectID, 0, 16),
			Reachable: make(map[ObjectID]struct{}, 256),
		},
	}
}

// Graph returns the shared process-wide object table.
func (o *LiveObjects) Graph() *ProcessGraph {
	if o == nil {
		return nil
	}
	return o.graph
}

// Info returns the compact goroutine reachability record.
func (o *LiveObjects) Info() GoroutineInfo {
	if o == nil {
		return GoroutineInfo{}
	}
	return o.info
}

// Add adds one root variable to this goroutine's reachability set.
// gid and frame identify the Delve scope used for lazy memory reads.
func (o *LiveObjects) Add(v *proc.Variable, gid int64, frame int) {
	if o == nil || v == nil {
		return
	}
	o.scopeGID = gid
	o.scopeFrame = frame
	o.addRuntimeRoots()
	o.walkFromRoots([]root{{v: v}})
}

// All returns all objects reachable from this goroutine.
func (o *LiveObjects) All() iter.Seq2[ObjectID, *Object] {
	return func(yield func(ObjectID, *Object) bool) {
		if o == nil || o.graph == nil {
			return
		}
		for id := range o.info.Reachable {
			obj := o.graph.Object(id)
			if obj == nil {
				continue
			}
			if !yield(id, obj) {
				return
			}
		}
	}
}

// ---- traversal internals (no printing) ----

type root struct {
	parent    ObjectID
	hasParent bool
	v         *proc.Variable
	knownID   ObjectID
	fromGraph bool
}

func (o *LiveObjects) walkFromRoots(roots []root) {
	stack := make([]root, 0, len(roots)+64)
	stack = append(stack, roots...)

	for len(stack) > 0 {
		n := len(stack) - 1
		cur := stack[n]
		stack = stack[:n]
		o.Stats.StackPops++

		if cur.fromGraph {
			o.markReachableAndPushKnown(&stack, cur.knownID)
			continue
		}

		v := cur.v
		if v == nil {
			continue
		}

		if isPointerLike(v) {
			ptr, ok := pointerValue(v)
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

			pointee := pointeeOrSelf(v)
			if pointee == v || len(pointee.Children) == 0 {
				if loaded := o.loadPointee(v, ptr); loaded != nil {
					pointee = loaded
				}
			}

			id, ok := o.graph.enterObject(ptr, pointee)
			if !ok {
				continue
			}
			o.noteEdgeOrRoot(cur, id)

			if !o.markReachable(id) {
				continue
			}
			if o.graph.isScanned(id) {
				o.Stats.DedupHits++
				pushKnownChildren(&stack, o.graph, id)
				continue
			}

			expanded := o.ensureLoaded(pointee)
			o.scanObjectChildren(&stack, id, expanded)
			continue
		}

		switch v.Kind {
		case reflect.Struct:
			if v.Addr != 0 {
				id, ok := o.graph.enterObject(uintptr(v.Addr), v)
				if !ok {
					continue
				}
				o.noteEdgeOrRoot(cur, id)

				if !o.markReachable(id) {
					continue
				}
				if o.graph.isScanned(id) {
					pushKnownChildren(&stack, o.graph, id)
					continue
				}

				expanded := o.ensureLoaded(v)
				o.scanObjectChildren(&stack, id, expanded)
				continue
			}
			pushChildren(&stack, cur.parent, cur.hasParent, v)

		case reflect.Array, reflect.Slice, reflect.Interface, reflect.Map, reflect.Chan:
			v = o.ensureLoaded(v)
			pushChildren(&stack, cur.parent, cur.hasParent, v)
		default:
			// primitives: nothing to do
		}
	}
}

func (o *LiveObjects) scanObjectChildren(stack *[]root, id ObjectID, v *proc.Variable) {
	o.graph.markScanned(id)
	pushChildren(stack, id, true, v)
}

func (o *LiveObjects) noteEdgeOrRoot(cur root, id ObjectID) {
	if cur.hasParent {
		o.graph.addChild(cur.parent, id)
		return
	}
	for _, existing := range o.info.Roots {
		if existing == id {
			return
		}
	}
	o.info.Roots = append(o.info.Roots, id)
}

func (o *LiveObjects) markReachable(id ObjectID) bool {
	if _, ok := o.info.Reachable[id]; ok {
		return false
	}
	o.info.Reachable[id] = struct{}{}
	return true
}

func (o *LiveObjects) markReachableAndPushKnown(stack *[]root, id ObjectID) {
	if !o.markReachable(id) {
		return
	}
	pushKnownChildren(stack, o.graph, id)
}

func pushKnownChildren(stack *[]root, graph *ProcessGraph, id ObjectID) {
	obj := graph.Object(id)
	if obj == nil {
		return
	}
	for i := len(obj.Children) - 1; i >= 0; i-- {
		*stack = append(*stack, root{knownID: obj.Children[i], fromGraph: true})
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

func pushChildren(stack *[]root, parent ObjectID, hasParent bool, v *proc.Variable) {
	if v == nil {
		return
	}
	// Push in reverse so Delve's child order is preserved by the LIFO stack.
	for i := len(v.Children) - 1; i >= 0; i-- {
		c := &v.Children[i]
		if v.Kind == reflect.Slice && (c.Name == "len" || c.Name == "cap") {
			continue
		}
		*stack = append(*stack, root{parent: parent, hasParent: hasParent, v: c})
	}
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
			if u, ok := constant.Uint64Val(v.Value); ok {
				return uintptr(u), true
			}
			if i, ok := constant.Int64Val(v.Value); ok && i >= 0 {
				return uintptr(i), true
			}
		}
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

func typeName(v *proc.Variable) string {
	if v == nil {
		return "<unknown>"
	}
	if ts := v.TypeString(); ts != "" {
		return ts
	}
	if v.Kind != reflect.Invalid {
		return v.Kind.String()
	}
	return "<unknown>"
}

func objectSize(v *proc.Variable) uint64 {
	if v == nil {
		return 0
	}
	if v.RealType != nil {
		if size := v.RealType.Size(); size > 0 {
			return uint64(size)
		}
	}
	if v.DwarfType != nil {
		if size := v.DwarfType.Size(); size > 0 {
			return uint64(size)
		}
	}
	return 0
}
