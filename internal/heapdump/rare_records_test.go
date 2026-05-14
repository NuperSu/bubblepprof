package heapdump

import (
	"testing"
)

// OSThread records carry (m addr, m id, os id). They are written by the
// runtime once per OS thread; parsing must populate snap.OSThreads.
func TestParseOSThread(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagOSThread)
	writeUvarint(buf, 0xaabb) // m addr
	writeUvarint(buf, 7)      // m id
	writeUvarint(buf, 4242)   // os id

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.OSThreads) != 1 {
		t.Fatalf("OSThreads = %+v", snap.OSThreads)
	}
	m := snap.OSThreads[0]
	if m.Addr != 0xaabb || m.ID != 7 || m.OSID != 4242 {
		t.Fatalf("OSThread = %+v", m)
	}
	if snap.Stats.OSThreadCount != 1 {
		t.Fatalf("OSThreadCount = %d", snap.Stats.OSThreadCount)
	}
}

// MemStats records are a long flat sequence of uvarints: a fixed set of
// named counters, then 256 pause_ns samples, then num_gc. The parser
// must materialize all of them as keys in snap.MemStats.
func TestParseMemStats(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagMemStats)
	// Named counters in order — values picked so they encode to distinct
	// uvarints. Order matches memStatsFields in parser.go.
	values := map[string]uint64{
		"alloc":          1,
		"total_alloc":    2,
		"sys":            3,
		"lookups":        4,
		"mallocs":        5,
		"frees":          6,
		"heap_alloc":     7,
		"heap_sys":       8,
		"heap_idle":      9,
		"heap_inuse":     10,
		"heap_released":  11,
		"heap_objects":   12,
		"stack_inuse":    13,
		"stack_sys":      14,
		"mspan_inuse":    15,
		"mspan_sys":      16,
		"mcache_inuse":   17,
		"mcache_sys":     18,
		"buckhash_sys":   19,
		"gc_sys":         20,
		"other_sys":      21,
		"next_gc":        22,
		"last_gc":        23,
		"pause_total_ns": 24,
	}
	for _, name := range memStatsFields {
		writeUvarint(buf, values[name])
	}
	for i := 0; i < 256; i++ {
		writeUvarint(buf, uint64(i)*100) // pause_ns_i
	}
	writeUvarint(buf, 42) // num_gc

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for name, want := range values {
		if got := snap.MemStats[name]; got != want {
			t.Fatalf("MemStats[%q] = %d, want %d", name, got, want)
		}
	}
	if snap.MemStats["pause_ns_0"] != 0 || snap.MemStats["pause_ns_255"] != 25500 {
		t.Fatalf("pause_ns samples not populated correctly")
	}
	if snap.MemStats["num_gc"] != 42 {
		t.Fatalf("num_gc = %d", snap.MemStats["num_gc"])
	}
}

// Defer records carry seven uvarint fields. Parser discards the values
// (they are not yet modeled) but must count them and not fail.
func TestParseDefer(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagDefer)
	for i := 0; i < 7; i++ {
		writeUvarint(buf, uint64(0x100+i))
	}

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Stats.DeferCount != 1 {
		t.Fatalf("DeferCount = %d", snap.Stats.DeferCount)
	}
}

// Panic records carry six uvarint fields. Same shape as defer: count
// and skip.
func TestParsePanic(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagPanic)
	for i := 0; i < 6; i++ {
		writeUvarint(buf, uint64(0x200+i))
	}

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Stats.PanicCount != 1 {
		t.Fatalf("PanicCount = %d", snap.Stats.PanicCount)
	}
}

// MemProf records carry: bucket addr, size, nstk, nstk * (funcname,
// file, line), allocs, frees. The parser must walk every frame
// regardless of nstk value, and must work for nstk == 0 too.
func TestParseMemProfWithFrames(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagMemProf)
	writeUvarint(buf, 0xdead) // bucket addr
	writeUvarint(buf, 64)     // size
	writeUvarint(buf, 2)      // nstk
	writeString(buf, "main.run")
	writeString(buf, "main.go")
	writeUvarint(buf, 12)
	writeString(buf, "runtime.goexit")
	writeString(buf, "asm_amd64.s")
	writeUvarint(buf, 1571)
	writeUvarint(buf, 5) // allocs
	writeUvarint(buf, 1) // frees

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Stats.MemProfCount != 1 {
		t.Fatalf("MemProfCount = %d", snap.Stats.MemProfCount)
	}
}

// MemProf record with zero frames must still consume the bucket/size/
// allocs/frees uvarints cleanly.
func TestParseMemProfZeroFrames(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagMemProf)
	writeUvarint(buf, 0xbeef) // bucket
	writeUvarint(buf, 16)     // size
	writeUvarint(buf, 0)      // nstk
	writeUvarint(buf, 0)      // allocs
	writeUvarint(buf, 0)      // frees

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Stats.MemProfCount != 1 {
		t.Fatalf("MemProfCount = %d", snap.Stats.MemProfCount)
	}
}

// AllocSample records are two uvarints: addr and bucket. Trivial but
// the dispatcher must recognize the tag.
func TestParseAllocSample(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagAllocSample)
	writeUvarint(buf, 0xabcd)
	writeUvarint(buf, 0xdef0)

	writeUvarint(buf, tagEOF)

	snap, err := Parse(buf, Options{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.Stats.AllocSampleCount != 1 {
		t.Fatalf("AllocSampleCount = %d", snap.Stats.AllocSampleCount)
	}
}

// Truncating a rare-record's payload must surface as a parse error
// rather than a panic, and the error must mention the failing field so
// callers can diagnose corrupt dumps.
func TestParseDeferTruncatedErrors(t *testing.T) {
	buf := newSyntheticBuffer()

	writeUvarint(buf, tagDefer)
	// Only 3 fields written; defer expects 7.
	for i := 0; i < 3; i++ {
		writeUvarint(buf, uint64(i))
	}
	// no tagEOF — let the parser hit EOF mid-record

	_, err := Parse(buf, Options{})
	if err == nil {
		t.Fatal("expected error on truncated defer record")
	}
}
