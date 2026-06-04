// Package heapsnapshot holds the normalized in-memory model produced by
// parsing a runtime/debug.WriteHeapDump output.
//
// The model is intentionally agnostic of how it was obtained. Higher-level
// phases convert it into a process graph or compute per-goroutine
// reachability. Phase 3 only fills it in.
package heapsnapshot

// FieldKind matches the kind tag used inside the heap dump field lists.
type FieldKind uint64

const (
	FieldKindEol   FieldKind = 0
	FieldKindPtr   FieldKind = 1
	FieldKindIface FieldKind = 2
	FieldKindEface FieldKind = 3
)

func (k FieldKind) String() string {
	switch k {
	case FieldKindEol:
		return "eol"
	case FieldKindPtr:
		return "ptr"
	case FieldKindIface:
		return "iface"
	case FieldKindEface:
		return "eface"
	default:
		return "unknown"
	}
}

// HeapSnapshot is the normalized model of a single heap dump.
type HeapSnapshot struct {
	Header string
	Params DumpParams

	Types      map[uint64]TypeInfo
	Itabs      map[uint64]Itab
	Objects    []Object
	Goroutines []Goroutine
	Globals    []Root
	Data       []DataSegment
	BSS        []DataSegment

	Finalizers       []Finalizer
	QueuedFinalizers []QueuedFinalizer
	OSThreads        []OSThread
	MemStats         map[string]uint64

	Stats    ParseStats
	Warnings []string
}

// DumpParams matches the `params` record at the start of every heap dump.
type DumpParams struct {
	BigEndian    bool
	PtrSize      int
	HeapStart    uint64
	HeapEnd      uint64
	GOARCH       string
	BuildVersion string
	NumCPU       uint64
}

// TypeInfo captures one runtime type descriptor seen in the dump.
type TypeInfo struct {
	Addr          uint64
	Size          uint64
	Name          string
	IndirectIface bool
	Fields        []Field
}

// Itab pairs an interface table address with its concrete type address.
type Itab struct {
	Addr     uint64
	TypeAddr uint64
}

// Field describes a single typed slot inside an object, stack frame, or
// data/bss segment.
type Field struct {
	Kind   FieldKind
	Offset uint64
}

// Object is one heap-allocated object discovered by the runtime.
type Object struct {
	Addr uint64
	Size uint64

	// Contents is empty unless Options.KeepObjectContents was set on the
	// parser. PointerAddrs already carries every direct pointer that was
	// decoded out of contents.
	Contents []byte

	Fields       []Field
	PointerAddrs []uint64
}

// Goroutine is one goroutine record. Frames are filled in as the parser
// reads `stack frame` records following the goroutine record.
type Goroutine struct {
	Addr         uint64
	StackTop     uint64
	ID           uint64
	GoPC         uint64
	Status       uint64
	IsSystem     bool
	IsBackground bool
	WaitSince    uint64
	WaitReason   string
	Context      uint64
	M            uint64
	Defer        uint64
	Panic        uint64

	Frames []StackFrame
}

// StackFrame is one frame within a goroutine's stack.
//
// PointerAddrs holds the decoded pointer values; PointerSlots holds the
// stack slot address (SP + field offset) where each pointer was read.
// The two slices are parallel and have equal length when the parser
// recorded slot addresses; consumers should range over PointerAddrs and
// only read PointerSlots[i] if i is in bounds, in case a future parser
// stops emitting slots for some frames.
type StackFrame struct {
	SP         uint64
	Depth      uint64
	ChildSP    uint64
	EntryPC    uint64
	CurrentPC  uint64
	ContinuePC uint64
	FuncName   string

	Size         uint64
	Fields       []Field
	PointerAddrs []uint64
	PointerSlots []uint64
}

// Root represents a single root pointer attribution. The address is the
// slot that holds the pointer; PointerAddr is the decoded pointer value.
type Root struct {
	Kind        string
	Description string
	Addr        uint64
	PointerAddr uint64
}

// DataSegment is one `data` or `bss` record.
//
// PointerAddrs and PointerSlots are parallel slices: PointerAddrs[i] is the
// decoded pointer value and PointerSlots[i] is the virtual address of the
// slot (Addr + field offset) that holds it. PointerSlots is populated by the
// parser; consumers must range over PointerAddrs and guard with
// i < len(PointerSlots) in case a future parser omits it.
type DataSegment struct {
	Kind         string
	Addr         uint64
	Size         uint64
	Fields       []Field
	PointerAddrs []uint64
	PointerSlots []uint64
}

// Finalizer is one `finalizer` record.
type Finalizer struct {
	ObjectAddr  uint64
	FuncVal     uint64
	FuncPC      uint64
	TypeAddr    uint64
	PtrTypeAddr uint64
}

// QueuedFinalizer is one `queued finalizer` record. The runtime emits the
// same fields as Finalizer but the meaning is "already queued for run".
type QueuedFinalizer struct {
	ObjectAddr  uint64
	FuncVal     uint64
	FuncPC      uint64
	TypeAddr    uint64
	PtrTypeAddr uint64
}

// OSThread is one `os thread` record.
type OSThread struct {
	Addr uint64
	ID   uint64
	OSID uint64
}

// ParseStats holds counters useful for summary output.
type ParseStats struct {
	ObjectCount      int
	ObjectBytes      uint64
	ObjectPointers   int
	TypeCount        int
	ItabCount        int
	GoroutineCount   int
	StackFrameCount  int
	StackPointers    int
	OtherRootCount   int
	DataCount        int
	DataPointers     int
	BSSCount         int
	BSSPointers      int
	GlobalRootCount  int
	FinalizerCount   int
	QueuedFinalizers int
	OSThreadCount    int
	DeferCount       int
	PanicCount       int
	MemProfCount     int
	AllocSampleCount int
	UnknownRecords   int

	// Iface/eface fields decoded by the parser. The data word of each
	// interface field is emitted as a candidate pointer; the graph resolver
	// drops any value that does not map to a known heap object.
	// For current Go runtime versions (1.24+) these counts are always zero
	// because the dump writer only emits fieldKindPtr from GC bitmaps.
	InterfaceFieldsDecoded int
	EfaceFieldsDecoded     int
}
