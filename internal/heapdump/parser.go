// Package heapdump parses the binary format written by
// runtime/debug.WriteHeapDump (see runtime/heapdump.go) into a normalized
// heapsnapshot.HeapSnapshot. The parser is intentionally independent of
// Delve and never tries to load the full heap dump into memory.
package heapdump

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// Options controls Parse behavior.
type Options struct {
	// KeepObjectContents retains raw object bytes inside each Object.
	// Off by default to avoid bringing back the old per-object memory
	// problem. PointerAddrs is always populated regardless.
	KeepObjectContents bool

	// MaxStringBytes is an upper bound on length-prefixed strings such as
	// function names and wait reasons. Zero uses the parser default.
	MaxStringBytes uint64

	// MaxMemRangeBytes is an upper bound on length-prefixed memory ranges
	// (object contents, frame contents, data/bss). Zero means no limit.
	MaxMemRangeBytes uint64

	// Strict turns recoverable problems (unknown field kinds, out-of-bounds
	// offsets) into errors instead of warnings.
	Strict bool
}

const (
	defaultMaxStringBytes = 16 << 20 // 16 MiB
)

// Parse reads a heap dump from r and returns the normalized snapshot.
//
// The reader is consumed in stream order; the caller does not need to
// preload the entire dump.
func Parse(r io.Reader, opts Options) (*heapsnapshot.HeapSnapshot, error) {
	snap, _, err := parseInto(r, opts, nil)
	return snap, err
}

// ParseLazyContents parses a heap dump from stream and returns a
// snapshot whose objects do not retain Contents bytes, together with a
// ContentResolver that can fetch any object's bytes on demand via ra.
//
// stream is consumed sequentially during Parse; ra must reference the
// same byte sequence (typically: pass the same *os.File for both, since
// *os.File implements both interfaces and ReadAt is independent of the
// file's current read offset on every supported platform).
//
// The returned resolver must not outlive ra. Callers that pass an
// *os.File must keep it open until the last call into the resolver.
//
// opts.KeepObjectContents is forced to false; passing true is silently
// ignored because the entire point of ParseLazyContents is to avoid
// retaining content bytes in the Go heap.
func ParseLazyContents(stream io.Reader, ra io.ReaderAt, opts Options) (*heapsnapshot.HeapSnapshot, *ContentResolver, error) {
	if ra == nil {
		return nil, nil, fmt.Errorf("heapdump: ParseLazyContents requires non-nil io.ReaderAt")
	}
	opts.KeepObjectContents = false
	tracker := &ContentResolver{src: ra}
	snap, resolver, err := parseInto(stream, opts, tracker)
	if err != nil {
		return nil, nil, err
	}
	return snap, resolver, nil
}

func parseInto(r io.Reader, opts Options, tracker *ContentResolver) (*heapsnapshot.HeapSnapshot, *ContentResolver, error) {
	if opts.MaxStringBytes == 0 {
		opts.MaxStringBytes = defaultMaxStringBytes
	}

	rd := newReader(r, Limits{
		MaxStringBytes:  opts.MaxStringBytes,
		MaxMemRangeSize: opts.MaxMemRangeBytes,
	})

	p := &parser{
		r:    rd,
		opts: opts,
		snap: &heapsnapshot.HeapSnapshot{
			Types:    make(map[uint64]heapsnapshot.TypeInfo),
			Itabs:    make(map[uint64]heapsnapshot.Itab),
			MemStats: make(map[string]uint64),
		},
		contentTracker: tracker,
	}

	if err := p.parseHeader(); err != nil {
		return nil, nil, err
	}
	if err := p.parseRecords(); err != nil {
		return nil, nil, err
	}
	if tracker != nil {
		tracker.finalize()
	}
	return p.snap, tracker, nil
}

type parser struct {
	r    *reader
	opts Options
	snap *heapsnapshot.HeapSnapshot

	// Once params is seen we know the endian and pointer size. Some files
	// in the wild might emit objects before params (we accept it but warn).
	haveParams bool
	byteOrder  binary.ByteOrder

	// Current goroutine receiving stack/defer/panic records.
	curG *heapsnapshot.Goroutine

	// contentTracker, when non-nil, receives (addr, file offset, length)
	// for every heap object record so callers can read object bytes on
	// demand instead of retaining them on snap.Objects[i].Contents.
	contentTracker *ContentResolver
}

func (p *parser) parseHeader() error {
	line, err := p.r.readLine()
	if err != nil {
		return fmt.Errorf("read heap dump header: %w", err)
	}
	if line != Header {
		return fmt.Errorf("unsupported heap dump header %q (want %q)", line, Header)
	}
	p.snap.Header = line
	return nil
}

func (p *parser) parseRecords() error {
	for {
		startOff := p.r.Offset()
		tag, err := p.r.Uvarint()
		if err == io.EOF {
			return fmt.Errorf("unexpected EOF before EOF tag at offset %d", startOff)
		}
		if err != nil {
			return fmt.Errorf("read record tag at offset %d: %w", startOff, err)
		}
		// Only stack frames, defers and panics belong to the current
		// goroutine. Any other record terminates the goroutine section
		// so a stray frame after, say, an osthread record is treated as
		// a parse mismatch instead of being attributed to the wrong g.
		switch tag {
		case tagStackFrame, tagDefer, tagPanic, tagGoroutine:
			// keep curG.
		default:
			p.endGoroutine()
		}

		switch tag {
		case tagEOF:
			return nil
		case tagParams:
			if err := p.parseParams(); err != nil {
				return p.wrap("params", startOff, err)
			}
		case tagType:
			if err := p.parseType(); err != nil {
				return p.wrap("type", startOff, err)
			}
		case tagObject:
			if err := p.parseObject(); err != nil {
				return p.wrap("object", startOff, err)
			}
		case tagOtherRoot:
			if err := p.parseOtherRoot(); err != nil {
				return p.wrap("otherroot", startOff, err)
			}
		case tagFinalizer:
			if err := p.parseFinalizer(); err != nil {
				return p.wrap("finalizer", startOff, err)
			}
		case tagItab:
			if err := p.parseItab(); err != nil {
				return p.wrap("itab", startOff, err)
			}
		case tagOSThread:
			if err := p.parseOSThread(); err != nil {
				return p.wrap("osthread", startOff, err)
			}
		case tagMemStats:
			if err := p.parseMemStats(); err != nil {
				return p.wrap("memstats", startOff, err)
			}
		case tagQueuedFinalizer:
			if err := p.parseQueuedFinalizer(); err != nil {
				return p.wrap("queued finalizer", startOff, err)
			}
		case tagData:
			if err := p.parseDataLike("data"); err != nil {
				return p.wrap("data", startOff, err)
			}
		case tagBSS:
			if err := p.parseDataLike("bss"); err != nil {
				return p.wrap("bss", startOff, err)
			}
		case tagGoroutine:
			if err := p.parseGoroutine(); err != nil {
				return p.wrap("goroutine", startOff, err)
			}
		case tagStackFrame:
			if err := p.parseStackFrame(); err != nil {
				return p.wrap("stack frame", startOff, err)
			}
		case tagDefer:
			if err := p.parseDefer(); err != nil {
				return p.wrap("defer", startOff, err)
			}
		case tagPanic:
			if err := p.parsePanic(); err != nil {
				return p.wrap("panic", startOff, err)
			}
		case tagMemProf:
			if err := p.parseMemProf(); err != nil {
				return p.wrap("mem prof", startOff, err)
			}
		case tagAllocSample:
			if err := p.parseAllocSample(); err != nil {
				return p.wrap("alloc sample", startOff, err)
			}
		default:
			// Unknown tags from a future runtime version cannot be skipped
			// because the record format is unknown. Stop here, but in
			// non-strict mode return the partial snapshot so callers can
			// still inspect what was parsed before the unknown tag.
			p.snap.Stats.UnknownRecords++
			if err := p.warn("unknown record tag %d at offset %d; stopping parse (snapshot is partial)", tag, startOff); err != nil {
				return err
			}
			return nil
		}
	}
}

func (p *parser) wrap(name string, off int64, err error) error {
	return fmt.Errorf("parse %s record at offset %d: %w", name, off, err)
}

func (p *parser) warn(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if p.opts.Strict {
		return fmt.Errorf("%s", msg)
	}
	p.snap.Warnings = append(p.snap.Warnings, msg)
	return nil
}

func (p *parser) endGoroutine() { p.curG = nil }

func (p *parser) parseParams() error {
	bigEndian, err := p.r.Bool()
	if err != nil {
		return fmt.Errorf("big endian: %w", err)
	}
	ptrSize, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("ptr size: %w", err)
	}
	if ptrSize != 4 && ptrSize != 8 {
		return fmt.Errorf("unsupported pointer size %d", ptrSize)
	}
	heapStart, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("heap start: %w", err)
	}
	heapEnd, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("heap end: %w", err)
	}
	goarch, err := p.r.String()
	if err != nil {
		return fmt.Errorf("goarch: %w", err)
	}
	buildVersion, err := p.r.String()
	if err != nil {
		return fmt.Errorf("build version: %w", err)
	}
	numCPU, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("num cpu: %w", err)
	}

	p.snap.Params = heapsnapshot.DumpParams{
		BigEndian:    bigEndian,
		PtrSize:      int(ptrSize),
		HeapStart:    heapStart,
		HeapEnd:      heapEnd,
		GOARCH:       goarch,
		BuildVersion: buildVersion,
		NumCPU:       numCPU,
	}
	p.haveParams = true
	if bigEndian {
		p.byteOrder = binary.BigEndian
	} else {
		p.byteOrder = binary.LittleEndian
	}
	return nil
}

func (p *parser) parseType() error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("type addr: %w", err)
	}
	size, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("type size: %w", err)
	}
	name, err := p.r.String()
	if err != nil {
		return fmt.Errorf("type name: %w", err)
	}
	indirectIface, err := p.r.Bool()
	if err != nil {
		return fmt.Errorf("indirect iface flag: %w", err)
	}
	p.snap.Types[addr] = heapsnapshot.TypeInfo{
		Addr:          addr,
		Size:          size,
		Name:          name,
		IndirectIface: indirectIface,
	}
	p.snap.Stats.TypeCount++
	return nil
}

func (p *parser) parseObject() error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("object addr: %w", err)
	}
	contents, contentFileOff, err := p.r.bytesWithFileOffset()
	if err != nil {
		return fmt.Errorf("object contents: %w", err)
	}
	fields, err := p.r.FieldList()
	if err != nil {
		return fmt.Errorf("object fields: %w", err)
	}

	if !p.haveParams {
		if err := p.warn("object record before params; pointer decoding skipped"); err != nil {
			return err
		}
	}

	var pointers []uint64
	if p.haveParams {
		var warnErr error
		iface, eface := extractPointers(contents, fields, p.snap.Params.PtrSize, p.byteOrder, addr,
			fmt.Sprintf("object 0x%x", addr),
			&pointers, nil, func(msg string) {
				if warnErr == nil {
					warnErr = p.warn("%s", msg)
				}
			})
		if warnErr != nil {
			return warnErr
		}
		p.snap.Stats.InterfaceFieldsDecoded += iface
		p.snap.Stats.EfaceFieldsDecoded += eface
	}

	obj := heapsnapshot.Object{
		Addr:         addr,
		Size:         uint64(len(contents)),
		Fields:       fields,
		PointerAddrs: pointers,
	}
	if p.opts.KeepObjectContents {
		obj.Contents = contents
	}
	if p.contentTracker != nil && len(contents) > 0 {
		p.contentTracker.record(addr, contentFileOff, uint64(len(contents)))
	}

	p.snap.Objects = append(p.snap.Objects, obj)
	p.snap.Stats.ObjectCount++
	p.snap.Stats.ObjectBytes += obj.Size
	p.snap.Stats.ObjectPointers += len(pointers)
	return nil
}

func (p *parser) parseOtherRoot() error {
	desc, err := p.r.String()
	if err != nil {
		return fmt.Errorf("description: %w", err)
	}
	ptr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("pointer: %w", err)
	}
	p.snap.Globals = append(p.snap.Globals, heapsnapshot.Root{
		Kind:        "otherroot",
		Description: desc,
		PointerAddr: ptr,
	})
	p.snap.Stats.OtherRootCount++
	p.snap.Stats.GlobalRootCount++
	return nil
}

func (p *parser) parseFinalizer() error {
	objAddr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("obj addr: %w", err)
	}
	fn, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fn addr: %w", err)
	}
	fnPC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fn pc: %w", err)
	}
	fint, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fint: %w", err)
	}
	ot, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("ot: %w", err)
	}
	p.snap.Finalizers = append(p.snap.Finalizers, heapsnapshot.Finalizer{
		ObjectAddr:  objAddr,
		FuncVal:     fn,
		FuncPC:      fnPC,
		TypeAddr:    fint,
		PtrTypeAddr: ot,
	})
	p.snap.Stats.FinalizerCount++
	return nil
}

func (p *parser) parseQueuedFinalizer() error {
	objAddr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("obj addr: %w", err)
	}
	fn, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fn addr: %w", err)
	}
	fnPC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fn pc: %w", err)
	}
	fint, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("fint: %w", err)
	}
	ot, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("ot: %w", err)
	}
	p.snap.QueuedFinalizers = append(p.snap.QueuedFinalizers, heapsnapshot.QueuedFinalizer{
		ObjectAddr:  objAddr,
		FuncVal:     fn,
		FuncPC:      fnPC,
		TypeAddr:    fint,
		PtrTypeAddr: ot,
	})
	p.snap.Stats.QueuedFinalizers++
	return nil
}

func (p *parser) parseItab() error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("itab addr: %w", err)
	}
	typeAddr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("type addr: %w", err)
	}
	p.snap.Itabs[addr] = heapsnapshot.Itab{Addr: addr, TypeAddr: typeAddr}
	p.snap.Stats.ItabCount++
	return nil
}

func (p *parser) parseOSThread() error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("m addr: %w", err)
	}
	id, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("m id: %w", err)
	}
	osid, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("os id: %w", err)
	}
	p.snap.OSThreads = append(p.snap.OSThreads, heapsnapshot.OSThread{
		Addr: addr,
		ID:   id,
		OSID: osid,
	})
	p.snap.Stats.OSThreadCount++
	return nil
}

// memStatsFields matches the order in runtime/heapdump.go:dumpmemstats and
// runtime.MemStats. The trailing PauseNs[256] and NumGC are stored under
// keys "pause_ns_0".."pause_ns_255" and "num_gc".
var memStatsFields = []string{
	"alloc", "total_alloc", "sys", "lookups", "mallocs", "frees",
	"heap_alloc", "heap_sys", "heap_idle", "heap_inuse", "heap_released",
	"heap_objects", "stack_inuse", "stack_sys", "mspan_inuse", "mspan_sys",
	"mcache_inuse", "mcache_sys", "buckhash_sys", "gc_sys", "other_sys",
	"next_gc", "last_gc", "pause_total_ns",
}

func (p *parser) parseMemStats() error {
	for _, name := range memStatsFields {
		v, err := p.r.Uvarint()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p.snap.MemStats[name] = v
	}
	for i := 0; i < 256; i++ {
		v, err := p.r.Uvarint()
		if err != nil {
			return fmt.Errorf("pause_ns[%d]: %w", i, err)
		}
		p.snap.MemStats[fmt.Sprintf("pause_ns_%d", i)] = v
	}
	numGC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("num_gc: %w", err)
	}
	p.snap.MemStats["num_gc"] = numGC
	return nil
}

func (p *parser) parseDataLike(kind string) error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("addr: %w", err)
	}
	contents, err := p.r.Bytes()
	if err != nil {
		return fmt.Errorf("contents: %w", err)
	}
	fields, err := p.r.FieldList()
	if err != nil {
		return fmt.Errorf("fields: %w", err)
	}

	seg := heapsnapshot.DataSegment{
		Kind:   kind,
		Addr:   addr,
		Size:   uint64(len(contents)),
		Fields: fields,
	}

	if p.haveParams {
		var targets, slots []uint64
		var warnErr error
		ctx := fmt.Sprintf("%s 0x%x", kind, addr)
		iface, eface := extractPointers(contents, fields, p.snap.Params.PtrSize, p.byteOrder, addr, ctx,
			&targets, &slots, func(msg string) {
				if warnErr == nil {
					warnErr = p.warn("%s", msg)
				}
			})
		if warnErr != nil {
			return warnErr
		}
		p.snap.Stats.InterfaceFieldsDecoded += iface
		p.snap.Stats.EfaceFieldsDecoded += eface
		seg.PointerAddrs = targets
		seg.PointerSlots = slots
		for i, target := range targets {
			var slot uint64
			if i < len(slots) {
				slot = slots[i]
			}
			p.snap.Globals = append(p.snap.Globals, heapsnapshot.Root{
				Kind:        kind,
				Addr:        slot,
				PointerAddr: target,
			})
		}
		p.snap.Stats.GlobalRootCount += len(targets)
		if kind == "data" {
			p.snap.Stats.DataPointers += len(targets)
		} else if kind == "bss" {
			p.snap.Stats.BSSPointers += len(targets)
		}
	}

	if kind == "data" {
		p.snap.Data = append(p.snap.Data, seg)
		p.snap.Stats.DataCount++
	} else {
		p.snap.BSS = append(p.snap.BSS, seg)
		p.snap.Stats.BSSCount++
	}
	return nil
}

func (p *parser) parseGoroutine() error {
	addr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("g addr: %w", err)
	}
	sp, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("sp: %w", err)
	}
	goid, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("goid: %w", err)
	}
	gopc, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("gopc: %w", err)
	}
	status, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	isSys, err := p.r.Bool()
	if err != nil {
		return fmt.Errorf("is system: %w", err)
	}
	isBg, err := p.r.Bool()
	if err != nil {
		return fmt.Errorf("is background: %w", err)
	}
	waitSince, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("wait since: %w", err)
	}
	waitReason, err := p.r.String()
	if err != nil {
		return fmt.Errorf("wait reason: %w", err)
	}
	ctxt, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("ctxt: %w", err)
	}
	m, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("m: %w", err)
	}
	deferAddr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("defer: %w", err)
	}
	panicAddr, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("panic: %w", err)
	}

	p.snap.Goroutines = append(p.snap.Goroutines, heapsnapshot.Goroutine{
		Addr:         addr,
		StackTop:     sp,
		ID:           goid,
		GoPC:         gopc,
		Status:       status,
		IsSystem:     isSys,
		IsBackground: isBg,
		WaitSince:    waitSince,
		WaitReason:   waitReason,
		Context:      ctxt,
		M:            m,
		Defer:        deferAddr,
		Panic:        panicAddr,
	})
	p.curG = &p.snap.Goroutines[len(p.snap.Goroutines)-1]
	p.snap.Stats.GoroutineCount++
	return nil
}

func (p *parser) parseStackFrame() error {
	sp, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("sp: %w", err)
	}
	depth, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("depth: %w", err)
	}
	childSP, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("child sp: %w", err)
	}
	contents, err := p.r.Bytes()
	if err != nil {
		return fmt.Errorf("contents: %w", err)
	}
	entryPC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("entry pc: %w", err)
	}
	curPC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("current pc: %w", err)
	}
	contPC, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("continpc: %w", err)
	}
	name, err := p.r.String()
	if err != nil {
		return fmt.Errorf("func name: %w", err)
	}
	fields, err := p.r.FieldList()
	if err != nil {
		return fmt.Errorf("fields: %w", err)
	}

	frame := heapsnapshot.StackFrame{
		SP:         sp,
		Depth:      depth,
		ChildSP:    childSP,
		EntryPC:    entryPC,
		CurrentPC:  curPC,
		ContinuePC: contPC,
		FuncName:   name,
		Size:       uint64(len(contents)),
		Fields:     fields,
	}

	if p.haveParams {
		var targets, slots []uint64
		var warnErr error
		ctx := fmt.Sprintf("frame %q (sp=0x%x)", name, sp)
		iface, eface := extractPointers(contents, fields, p.snap.Params.PtrSize, p.byteOrder, sp, ctx,
			&targets, &slots, func(msg string) {
				if warnErr == nil {
					warnErr = p.warn("%s", msg)
				}
			})
		if warnErr != nil {
			return warnErr
		}
		p.snap.Stats.InterfaceFieldsDecoded += iface
		p.snap.Stats.EfaceFieldsDecoded += eface
		frame.PointerAddrs = targets
		frame.PointerSlots = slots
	}

	if p.curG == nil {
		if err := p.warn("stack frame %q at sp=0x%x has no enclosing goroutine; dropped", name, sp); err != nil {
			return err
		}
		return nil
	}
	p.curG.Frames = append(p.curG.Frames, frame)
	p.snap.Stats.StackFrameCount++
	p.snap.Stats.StackPointers += len(frame.PointerAddrs)
	return nil
}

func (p *parser) parseDefer() error {
	// d, gp, sp, pc, fn, fn.fn (entry pc), link.
	for _, field := range []string{"d", "gp", "sp", "pc", "fn", "fn_pc", "link"} {
		if _, err := p.r.Uvarint(); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	p.snap.Stats.DeferCount++
	return nil
}

func (p *parser) parsePanic() error {
	// p, gp, eface._type, eface.data, 0 (reserved), link.
	for _, field := range []string{"p", "gp", "arg_type", "arg_data", "reserved", "link"} {
		if _, err := p.r.Uvarint(); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	p.snap.Stats.PanicCount++
	return nil
}

func (p *parser) parseMemProf() error {
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("bucket addr: %w", err)
	}
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("size: %w", err)
	}
	nstk, err := p.r.Uvarint()
	if err != nil {
		return fmt.Errorf("nstk: %w", err)
	}
	for i := uint64(0); i < nstk; i++ {
		if _, err := p.r.String(); err != nil {
			return fmt.Errorf("frame[%d] func name: %w", i, err)
		}
		if _, err := p.r.String(); err != nil {
			return fmt.Errorf("frame[%d] file: %w", i, err)
		}
		if _, err := p.r.Uvarint(); err != nil {
			return fmt.Errorf("frame[%d] line: %w", i, err)
		}
	}
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("allocs: %w", err)
	}
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("frees: %w", err)
	}
	p.snap.Stats.MemProfCount++
	return nil
}

func (p *parser) parseAllocSample() error {
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("addr: %w", err)
	}
	if _, err := p.r.Uvarint(); err != nil {
		return fmt.Errorf("bucket: %w", err)
	}
	p.snap.Stats.AllocSampleCount++
	return nil
}
