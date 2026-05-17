package heapdump

import (
	"bytes"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

// writeMinimalGoroutine writes a complete goroutine record to buf.
func writeMinimalGoroutine(buf *bytes.Buffer, addr, goid uint64) {
	writeUvarint(buf, tagGoroutine)
	writeUvarint(buf, addr) // g addr
	writeUvarint(buf, 0)    // sp
	writeUvarint(buf, goid) // goid
	writeUvarint(buf, 0)    // gopc
	writeUvarint(buf, 0)    // status
	writeBool(buf, false)   // isSys
	writeBool(buf, false)   // isBg
	writeUvarint(buf, 0)    // waitSince
	writeString(buf, "")    // waitReason
	writeUvarint(buf, 0)    // ctxt
	writeUvarint(buf, 0)    // m
	writeUvarint(buf, 0)    // deferAddr
	writeUvarint(buf, 0)    // panicAddr
}

// writeMinimalStackFrame writes a complete stack frame record to buf.
func writeMinimalStackFrame(buf *bytes.Buffer, sp uint64, name string) {
	writeUvarint(buf, tagStackFrame)
	writeUvarint(buf, sp)      // sp
	writeUvarint(buf, 0)       // depth
	writeUvarint(buf, 0)       // childSP
	writeBytes(buf, []byte{})  // contents (empty)
	writeUvarint(buf, 0)       // entryPC
	writeUvarint(buf, 0)       // curPC
	writeUvarint(buf, 0)       // contPC
	writeString(buf, name)     // func name
	writeFieldList(buf, nil)   // no fields
}

// TestParseRecords_TruncatedAtFirstField exercises the p.wrap(...) dispatch in
// parseRecords by writing each record tag followed by no payload. The parser
// must return an error (not panic) for every truncated record.
func TestParseRecords_TruncatedAtFirstField(t *testing.T) {
	tags := []struct {
		name string
		tag  uint64
	}{
		{"type", tagType},
		{"otherroot", tagOtherRoot},
		{"finalizer", tagFinalizer},
		{"queuedFinalizer", tagQueuedFinalizer},
		{"itab", tagItab},
		{"osthread", tagOSThread},
		{"memstats", tagMemStats},
		{"data", tagData},
		{"bss", tagBSS},
		{"goroutine", tagGoroutine},
		{"stackframe", tagStackFrame},
		{"panic", tagPanic},
		{"memprof", tagMemProf},
		{"allocsample", tagAllocSample},
		// A second tagParams record triggers p.wrap("params", ...) in dispatch.
		{"params2", tagParams},
	}
	for _, tc := range tags {
		t.Run(tc.name, func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tc.tag)
			// No fields written; parser hits EOF mid-record.
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error for truncated %q record, got nil", tc.name)
			}
		})
	}
}

// TestParseRecords_UnexpectedEOF verifies that a dump missing the final EOF
// tag produces an error mentioning "unexpected EOF".
func TestParseRecords_UnexpectedEOF(t *testing.T) {
	buf := newSyntheticBuffer()
	// No tagEOF: parseRecords reads the next tag, hits io.EOF → error.
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected unexpected-EOF error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("error = %q, want 'unexpected EOF'", err.Error())
	}
}

// TestParseRecords_UnknownTag verifies that an unknown tag in non-strict mode
// stops parsing and increments UnknownRecords.
func TestParseRecords_UnknownTag(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, 255) // unknown tag
	// No EOF: parser stops at unknown tag.
	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error in non-strict mode: %v", err)
	}
	if snap.Stats.UnknownRecords != 1 {
		t.Fatalf("UnknownRecords = %d, want 1", snap.Stats.UnknownRecords)
	}
}

// TestParseRecords_UnknownTagStrict verifies that an unknown tag in strict
// mode causes Parse to return an error.
func TestParseRecords_UnknownTagStrict(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, 255) // unknown tag
	_, err := Parse(buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected error in strict mode for unknown tag, got nil")
	}
}

// TestParseStackFrame_OrphanFrame verifies that a stack frame with no
// enclosing goroutine emits a warning and is dropped (not appended to any g).
func TestParseStackFrame_OrphanFrame(t *testing.T) {
	buf := newSyntheticBuffer()
	// Write a goroutine so curG is set.
	writeMinimalGoroutine(buf, 0x1000, 1)
	// Write an OSThread record — this calls endGoroutine() → curG = nil.
	writeUvarint(buf, tagOSThread)
	writeUvarint(buf, 0xdead) // m addr
	writeUvarint(buf, 7)      // m id
	writeUvarint(buf, 42)     // os id
	// Now write a stack frame: no enclosing goroutine → warning + drop.
	writeMinimalStackFrame(buf, 0x8000, "orphanFunc")
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Goroutine has no frames (the orphan frame was dropped).
	if len(snap.Goroutines) != 1 || len(snap.Goroutines[0].Frames) != 0 {
		t.Fatalf("goroutine frames = %d, want 0", len(snap.Goroutines[0].Frames))
	}
	if !hasParseWarning(snap.Warnings, "no enclosing goroutine") {
		t.Fatalf("expected orphan-frame warning, got %v", snap.Warnings)
	}
}

// TestParseStackFrame_OrphanFrameStrict verifies that an orphan frame causes
// an error in strict mode.
func TestParseStackFrame_OrphanFrameStrict(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagOSThread)
	writeUvarint(buf, 0xdead)
	writeUvarint(buf, 7)
	writeUvarint(buf, 42)
	writeMinimalStackFrame(buf, 0x8000, "orphanFunc")
	writeUvarint(buf, tagEOF)

	_, err := Parse(buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected error in strict mode for orphan frame, got nil")
	}
}

// TestParseObject_BeforeParams verifies that an object record before any
// params record emits a warning in non-strict mode.
func TestParseObject_BeforeParams(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	// tagObject without preceding tagParams.
	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x1000)   // addr
	writeBytes(&buf, []byte{})   // empty contents
	writeFieldList(&buf, nil)    // no fields
	writeUvarint(&buf, tagEOF)

	snap, err := Parse(&buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasParseWarning(snap.Warnings, "before params") {
		t.Fatalf("expected 'before params' warning, got %v", snap.Warnings)
	}
}

// TestParseObject_BeforeParamsStrict verifies that an object before params
// returns an error in strict mode.
func TestParseObject_BeforeParamsStrict(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x1000)
	writeBytes(&buf, []byte{})
	writeFieldList(&buf, nil)
	writeUvarint(&buf, tagEOF)

	_, err := Parse(&buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected error in strict mode for object before params")
	}
}

// TestParseObject_IfaceEfaceFields verifies that iface and eface field kinds
// increment the skip counters.
func TestParseObject_IfaceEfaceFields(t *testing.T) {
	buf := newSyntheticBuffer()

	// Object: 16 bytes of contents, fields: iface @ 0, eface @ 0, eol
	// (no ptr field so extractPointers completes and returns accumulated counts)
	contents := make([]byte, 16)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, contents)
	// Write field list manually: iface, eface, eol
	writeUvarint(buf, uint64(heapsnapshot.FieldKindIface))
	writeUvarint(buf, 0) // offset
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEface))
	writeUvarint(buf, 0) // offset
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Stats.InterfaceFieldsSkipped != 1 {
		t.Fatalf("InterfaceFieldsSkipped = %d, want 1", snap.Stats.InterfaceFieldsSkipped)
	}
	if snap.Stats.EfaceFieldsSkipped != 1 {
		t.Fatalf("EfaceFieldsSkipped = %d, want 1", snap.Stats.EfaceFieldsSkipped)
	}
}

// TestParseObject_BadPtrOffset verifies that a ptr field whose offset+ptrSize
// extends beyond the object contents triggers a warning in non-strict mode.
func TestParseObject_BadPtrOffset(t *testing.T) {
	buf := newSyntheticBuffer()

	// Object: 8 bytes; ptr field at offset 4 with ptrSize=8 → end=12 > 8 → warn
	contents := make([]byte, 8)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, contents)
	writeUvarint(buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(buf, 4)
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasParseWarning(snap.Warnings, "unsupported ptr size") && !hasParseWarning(snap.Warnings, "object 0x1000") {
		t.Fatalf("expected ptr warning, got %v", snap.Warnings)
	}
}

// TestReadPointer_OffsetBeyondContents verifies the offset > len(contents)
// path in readPointer (distinct from ptrSize + offset > len).
func TestReadPointer_OffsetBeyondContents(t *testing.T) {
	buf := newSyntheticBuffer()
	// 4-byte contents, ptr field at offset 5 (5 > 4) → readPointer: offset > size
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x3000)
	writeBytes(buf, make([]byte, 4)) // 4 bytes
	writeUvarint(buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(buf, 5) // offset 5 > len 4
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasParseWarning(snap.Warnings, "out of bounds") {
		t.Fatalf("expected out-of-bounds warning, got %v", snap.Warnings)
	}
}

// TestExtractPointers_UnsupportedPtrSize exercises the ptrSize != 4 && != 8
// early-exit path via a stack frame with haveParams=false (so ptrSize is read
// from a custom params with PtrSize=2 — which the parser rejects, but we can
// test extractPointers indirectly via a stack frame when params use PtrSize=4).
// Instead, test via a data segment that provides a field when ptrSize comes from
// a param record with PtrSize=4 and offset is well-formed.
func TestExtractPointers_PtrSize4Path(t *testing.T) {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		PtrSize:      4,
		BigEndian:    false,
		GOARCH:       "386",
		BuildVersion: "go-test",
		NumCPU:       1,
	})
	// Object with 4 bytes, ptr at offset 0 with ptrSize=4.
	writeUvarint(&buf, tagObject)
	writeUvarint(&buf, 0x1000)
	writeBytes(&buf, []byte{0x01, 0x00, 0x00, 0x00}) // 4 bytes
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(&buf, 0) // offset 0 + ptrSize 4 = 4 == len 4 → valid
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(&buf, tagEOF)

	snap, err := Parse(&buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Ptr value = 1 (little-endian 0x01000000... wait, little endian: 0x01000000 = 1)
	if len(snap.Objects) != 1 || len(snap.Objects[0].PointerAddrs) != 1 {
		t.Fatalf("objects=%d pointers=%d", len(snap.Objects), len(snap.Objects[0].PointerAddrs))
	}
}

// TestParseObject_UnknownFieldKind verifies that an unknown field kind
// triggers a warning rather than a parse error.
func TestParseObject_UnknownFieldKind(t *testing.T) {
	buf := newSyntheticBuffer()

	contents := make([]byte, 8)
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x2000)
	writeBytes(buf, contents)
	// Field kind 99 is unknown.
	writeUvarint(buf, 99)                                   // unknown kind
	writeUvarint(buf, 0)                                    // offset
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))   // terminate
	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasParseWarning(snap.Warnings, "unknown field kind") {
		t.Fatalf("expected unknown-field-kind warning, got %v", snap.Warnings)
	}
}

// TestParseGoroutine_AllFieldTruncations exercises every error-return path
// inside parseGoroutine by truncating the record at each successive field.
func TestParseGoroutine_AllFieldTruncations(t *testing.T) {
	// Each entry is a write function for one goroutine field.
	fields := []func(*bytes.Buffer){
		func(b *bytes.Buffer) { writeUvarint(b, 0x1000) }, // g addr
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // sp
		func(b *bytes.Buffer) { writeUvarint(b, 1) },      // goid
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // gopc
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // status
		func(b *bytes.Buffer) { writeBool(b, false) },     // isSys
		func(b *bytes.Buffer) { writeBool(b, false) },     // isBg
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // waitSince
		func(b *bytes.Buffer) { writeString(b, "") },      // waitReason
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // ctxt
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // m
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // deferAddr
		// panicAddr not included → truncated record
	}
	for n := range fields {
		n := n
		t.Run("truncate_at_field_"+itoa10(n+1), func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tagGoroutine)
			for i := 0; i < n; i++ {
				fields[i](buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error when truncated after %d field(s)", n)
			}
		})
	}
}

// TestParseStackFrame_AllFieldTruncations exercises every error-return path
// inside parseStackFrame by truncating at each successive field.
func TestParseStackFrame_AllFieldTruncations(t *testing.T) {
	fields := []func(*bytes.Buffer){
		func(b *bytes.Buffer) { writeUvarint(b, 0x8000) }, // sp
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // depth
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // childSP
		func(b *bytes.Buffer) { writeBytes(b, []byte{}) }, // contents
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // entryPC
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // curPC
		func(b *bytes.Buffer) { writeUvarint(b, 0) },      // contPC
		func(b *bytes.Buffer) { writeString(b, "f") },     // name
		// fields (field list) not written → truncated
	}
	for n := range fields {
		n := n
		t.Run("truncate_at_field_"+itoa10(n+1), func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeMinimalGoroutine(buf, 0x1000, 1)  // need curG set
			writeUvarint(buf, tagStackFrame)
			for i := 0; i < n; i++ {
				fields[i](buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error when truncated after %d field(s)", n)
			}
		})
	}
}

// TestParseMemProf_AllFieldTruncations exercises every error-return path in
// parseMemProf by truncating at each successive field (nstk=1 for the loop path).
func TestParseMemProf_AllFieldTruncations(t *testing.T) {
	fields := []func(*bytes.Buffer){
		func(b *bytes.Buffer) { writeUvarint(b, 0xdead) }, // bucket addr
		func(b *bytes.Buffer) { writeUvarint(b, 64) },     // size
		func(b *bytes.Buffer) { writeUvarint(b, 1) },      // nstk=1
		func(b *bytes.Buffer) { writeString(b, "f") },     // frame[0] func name
		func(b *bytes.Buffer) { writeString(b, "a.go") },  // frame[0] file
		func(b *bytes.Buffer) { writeUvarint(b, 10) },     // frame[0] line
		func(b *bytes.Buffer) { writeUvarint(b, 5) },      // allocs
		// frees not written → truncated
	}
	for n := range fields {
		n := n
		t.Run("truncate_at_field_"+itoa10(n+1), func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tagMemProf)
			for i := 0; i < n; i++ {
				fields[i](buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error when truncated after %d field(s)", n)
			}
		})
	}
}

// TestParseFinalizer_LateFieldTruncations covers error paths for the later
// fields of parseFinalizer / parseQueuedFinalizer.
func TestParseFinalizer_LateFieldTruncations(t *testing.T) {
	for _, tc := range []struct{ name string; tag uint64 }{
		{"finalizer", tagFinalizer},
		{"queuedfinalizer", tagQueuedFinalizer},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Write obj + fn, truncate at fnPC.
			buf := newSyntheticBuffer()
			writeUvarint(buf, tc.tag)
			writeUvarint(buf, 0x1000) // obj addr
			writeUvarint(buf, 0x2000) // fn addr
			// fnPC missing → error
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatal("expected error for truncated finalizer record")
			}
		})
	}
}

// TestParseOSThread_LateTruncation covers the m id error path in parseOSThread.
func TestParseOSThread_LateTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagOSThread)
	writeUvarint(buf, 0xdead) // m addr
	// m id missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated OSThread record")
	}
}

// TestParseAllocSample_LateTruncation covers the bucket error path.
func TestParseAllocSample_LateTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagAllocSample)
	writeUvarint(buf, 0xabcd) // addr
	// bucket missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated AllocSample record")
	}
}

// TestParseItab_LateTruncation covers the type addr error path.
func TestParseItab_LateTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagItab)
	writeUvarint(buf, 0x1000) // itab addr
	// type addr missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated Itab record")
	}
}

// TestParseType_LateFieldTruncations covers parseType error paths after addr.
func TestParseType_LateFieldTruncations(t *testing.T) {
	type truncPoint struct {
		name   string
		fields []func(*bytes.Buffer)
	}
	cases := []truncPoint{
		{
			"at_size",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeUvarint(b, 0x1000) }, // addr only
			},
		},
		{
			"at_name",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeUvarint(b, 0x1000) }, // addr
				func(b *bytes.Buffer) { writeUvarint(b, 16) },     // size
			},
		},
		{
			"at_indirect",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeUvarint(b, 0x1000) }, // addr
				func(b *bytes.Buffer) { writeUvarint(b, 16) },     // size
				func(b *bytes.Buffer) { writeString(b, "T") },     // name
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tagType)
			for _, f := range tc.fields {
				f(buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseParams_LateFieldTruncations covers parseParams error paths for fields
// after bigEndian (tested by triggering a second params record in the record loop).
func TestParseParams_LateFieldTruncations(t *testing.T) {
	type truncPoint struct {
		name   string
		fields []func(*bytes.Buffer)
	}
	cases := []truncPoint{
		{
			"at_ptrSize",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) }, // bigEndian
			},
		},
		{
			"at_heapStart",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) },  // bigEndian
				func(b *bytes.Buffer) { writeUvarint(b, 8) },   // ptrSize
			},
		},
		{
			"at_heapEnd",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) },  // bigEndian
				func(b *bytes.Buffer) { writeUvarint(b, 8) },   // ptrSize
				func(b *bytes.Buffer) { writeUvarint(b, 0) },   // heapStart
			},
		},
		{
			"at_goarch",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) },  // bigEndian
				func(b *bytes.Buffer) { writeUvarint(b, 8) },   // ptrSize
				func(b *bytes.Buffer) { writeUvarint(b, 0) },   // heapStart
				func(b *bytes.Buffer) { writeUvarint(b, 0) },   // heapEnd
			},
		},
		{
			"at_buildVersion",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) },       // bigEndian
				func(b *bytes.Buffer) { writeUvarint(b, 8) },        // ptrSize
				func(b *bytes.Buffer) { writeUvarint(b, 0) },        // heapStart
				func(b *bytes.Buffer) { writeUvarint(b, 0) },        // heapEnd
				func(b *bytes.Buffer) { writeString(b, "amd64") },   // goarch
			},
		},
		{
			"at_numCPU",
			[]func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeBool(b, false) },         // bigEndian
				func(b *bytes.Buffer) { writeUvarint(b, 8) },          // ptrSize
				func(b *bytes.Buffer) { writeUvarint(b, 0) },          // heapStart
				func(b *bytes.Buffer) { writeUvarint(b, 0) },          // heapEnd
				func(b *bytes.Buffer) { writeString(b, "amd64") },     // goarch
				func(b *bytes.Buffer) { writeString(b, "go1.26.0") },  // buildVersion
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Use a second params tag in the main record loop.
			buf := newSyntheticBuffer()
			writeUvarint(buf, tagParams) // second params record
			for _, f := range tc.fields {
				f(buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error for truncated params at %s", tc.name)
			}
		})
	}
}

// TestParseDataLike_Truncations covers error paths in parseDataLike (data/bss).
func TestParseDataLike_Truncations(t *testing.T) {
	for _, tag := range []uint64{tagData, tagBSS} {
		tag := tag
		t.Run("bss_or_data", func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tag)
			writeUvarint(buf, 0x1000) // addr
			// contents missing → error
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatal("expected error for truncated data/bss record")
			}
		})
	}
}

// TestParseOtherRoot_LateTruncation covers the pointer error path.
func TestParseOtherRoot_LateTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagOtherRoot)
	writeString(buf, "some root") // description
	// pointer addr missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated OtherRoot record")
	}
}

// TestParseMemStats_PauseNSTruncation covers the pause_ns loop error path.
func TestParseMemStats_PauseNSTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagMemStats)
	// Write all named counter fields.
	for range memStatsFields {
		writeUvarint(buf, 0)
	}
	// Write only the first 10 pause_ns entries (expects 256) → truncated
	for i := 0; i < 10; i++ {
		writeUvarint(buf, 0)
	}
	// (pause_ns[10] missing → error)
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated MemStats pause_ns")
	}
}

// TestParseObject_TruncatedAtContents covers the "object contents" error path.
func TestParseObject_TruncatedAtContents(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000) // addr
	// contents missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated object at contents")
	}
}

// TestParseFinalizer_AllTruncations covers all error paths in parseFinalizer
// and parseQueuedFinalizer.
func TestParseFinalizer_AllTruncations(t *testing.T) {
	for _, tc := range []struct{ name string; tag uint64 }{
		{"finalizer", tagFinalizer},
		{"queuedfinalizer", tagQueuedFinalizer},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			finFields := []func(*bytes.Buffer){
				func(b *bytes.Buffer) { writeUvarint(b, 0x1000) }, // obj
				func(b *bytes.Buffer) { writeUvarint(b, 0x2000) }, // fn
				func(b *bytes.Buffer) { writeUvarint(b, 0x3000) }, // fnPC
				func(b *bytes.Buffer) { writeUvarint(b, 0x4000) }, // fint
				// ot not written → truncated
			}
			for n := range finFields {
				n := n
				t.Run(itoa10(n), func(t *testing.T) {
					buf := newSyntheticBuffer()
					writeUvarint(buf, tc.tag)
					for i := 0; i < n; i++ {
						finFields[i](buf)
					}
					_, err := Parse(buf, Options{})
					if err == nil {
						t.Fatalf("expected error at field %d", n)
					}
				})
			}
		})
	}
}

// TestParseOSThread_AllTruncations covers the os id error path.
func TestParseOSThread_AllTruncations(t *testing.T) {
	osFields := []func(*bytes.Buffer){
		func(b *bytes.Buffer) { writeUvarint(b, 0xdead) }, // m addr
		func(b *bytes.Buffer) { writeUvarint(b, 7) },      // m id
		// os id not written → truncated
	}
	for n := range osFields {
		n := n
		t.Run(itoa10(n), func(t *testing.T) {
			buf := newSyntheticBuffer()
			writeUvarint(buf, tagOSThread)
			for i := 0; i < n; i++ {
				osFields[i](buf)
			}
			_, err := Parse(buf, Options{})
			if err == nil {
				t.Fatalf("expected error at field %d", n)
			}
		})
	}
}

// TestParseDataLike_FieldsTruncation covers the fields error path after contents.
func TestParseDataLike_FieldsTruncation(t *testing.T) {
	for _, tag := range []uint64{tagData, tagBSS} {
		tag := tag
		buf := newSyntheticBuffer()
		writeUvarint(buf, tag)
		writeUvarint(buf, 0x1000)    // addr
		writeBytes(buf, []byte{0x0}) // non-empty contents (1 byte)
		// fields (FieldList) missing → error
		_, err := Parse(buf, Options{})
		if err == nil {
			t.Fatalf("expected error for truncated data fields (tag %d)", tag)
		}
	}
}

// TestParseMemStats_NumGCTruncation covers the num_gc error path.
func TestParseMemStats_NumGCTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagMemStats)
	for range memStatsFields {
		writeUvarint(buf, 0)
	}
	for i := 0; i < 256; i++ {
		writeUvarint(buf, 0) // pause_ns
	}
	// num_gc missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated MemStats num_gc")
	}
}

// TestParseObject_WarnErrInStrictMode verifies that a ptr-field overflow in
// strict mode causes parseObject to return an error.
func TestParseObject_WarnErrInStrictMode(t *testing.T) {
	buf := newSyntheticBuffer()
	contents := make([]byte, 4) // only 4 bytes, ptrSize=8 → any ptr field fails
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, contents)
	writeUvarint(buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(buf, 0) // offset 0 + ptrSize 8 > len 4 → warn
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(buf, tagEOF)

	_, err := Parse(buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected error in strict mode for overflowing ptr field")
	}
}

// TestParseStackFrame_FieldListTruncation covers the fields error path.
func TestParseStackFrame_FieldListTruncation(t *testing.T) {
	buf := newSyntheticBuffer()
	writeMinimalGoroutine(buf, 0x1000, 1)
	writeUvarint(buf, tagStackFrame)
	writeUvarint(buf, 0x8000)     // sp
	writeUvarint(buf, 0)          // depth
	writeUvarint(buf, 0)          // childSP
	writeBytes(buf, []byte{})     // contents
	writeUvarint(buf, 0)          // entryPC
	writeUvarint(buf, 0)          // curPC
	writeUvarint(buf, 0)          // contPC
	writeString(buf, "func")      // name
	// FieldList missing → error
	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated stackframe fields")
	}
}

// TestParseStackFrame_WarnErrInStrictMode verifies that a bad ptr in frame
// contents triggers an error in strict mode.
func TestParseStackFrame_WarnErrInStrictMode(t *testing.T) {
	buf := newSyntheticBuffer()
	writeMinimalGoroutine(buf, 0x1000, 1)
	// Frame: 4 bytes contents, ptr at offset 0 with ptrSize=8 > 4 → overflow → warn
	writeUvarint(buf, tagStackFrame)
	writeUvarint(buf, 0x8000)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeBytes(buf, make([]byte, 4)) // only 4 bytes
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeUvarint(buf, 0)
	writeString(buf, "f")
	// Field list: ptr at offset 0 → overflow
	writeUvarint(buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(buf, 0) // offset 0 + 8 > 4 → fail
	writeUvarint(buf, uint64(heapsnapshot.FieldKindEol))
	writeUvarint(buf, tagEOF)

	_, err := Parse(buf, Options{Strict: true})
	if err == nil {
		t.Fatal("expected error in strict mode for overflowing ptr in stack frame")
	}
}

// itoa10 is a simple int-to-string helper (no strconv import needed).
func itoa10(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestParseHeader_Truncated verifies that an empty reader produces a parse error.
func TestParseHeader_Truncated(t *testing.T) {
	var buf bytes.Buffer // empty
	_, err := Parse(&buf, Options{})
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
}

// TestParseHeader_Invalid verifies that a wrong header string produces a parse error.
func TestParseHeader_Invalid(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("wrong header\n")
	_, err := Parse(&buf, Options{})
	if err == nil {
		t.Fatal("expected error for invalid header, got nil")
	}
	if !strings.Contains(err.Error(), "header") {
		t.Fatalf("error = %q, want to mention 'header'", err.Error())
	}
}
