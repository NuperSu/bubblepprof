package heaplabels

import (
	"testing"

	"bubblepprof/internal/addrspace"
	"bubblepprof/internal/heapsnapshot"
)

// extraReader is a minimal addrspace.Reader backed by a single fixed
// byte range. The Phase 3 fallback test uses it to simulate label
// string bytes that live outside heap dump object contents (the same
// situation literal pprof.Labels strings create at runtime).
type extraReader struct {
	base uint64
	data []byte
}

func (e *extraReader) ReadAtAddr(addr, size uint64) ([]byte, bool) {
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end := addr + size
	if end < addr {
		return nil, false
	}
	regionEnd := e.base + uint64(len(e.data))
	if addr < e.base || end > regionEnd {
		return nil, false
	}
	out := make([]byte, size)
	copy(out, e.data[addr-e.base:end-e.base])
	return out, true
}

func (e *extraReader) Name() string { return "test-extra" }

// TestDecodeAll_ExtraMemoryRecoversLiteralStringBytes builds a synthetic
// snapshot where the runtime.g, labelMap, label array, and string
// HEADERS all live in heap object contents, but the actual label
// string BYTES (key/value characters) live ONLY in an external
// addrspace.Reader — the exact shape ordinary `pprof.Labels("job","42")`
// produces, where the strings sit in executable rodata.
//
// With ExtraMemory unset, the decoder must report StatusStringMissing.
// With ExtraMemory wired in, decoding must succeed.
func TestDecodeAll_ExtraMemoryRecoversLiteralStringBytes(t *testing.T) {
	gAddr := uint64(0x5000)
	labelMapAddr := uint64(0x1000)
	labelArrayAddr := uint64(0x2000)

	const keyAddr uint64 = 0x800000
	const valAddr uint64 = 0x800100
	const keyStr = "job"
	const valStr = "alpha"

	// Heap-resident structural bytes only — no string bodies.
	gObj := make([]byte, 0x200)
	writePtr(gObj, 0x18, labelMapAddr) // runtime.g.labels at offset 0x18

	labelMap := make([]byte, 24)
	writePtr(labelMap, 0, labelArrayAddr)
	writePtr(labelMap, 8, 1)
	writePtr(labelMap, 16, 1)

	labelArray := make([]byte, 32)
	writeStringHeader(labelArray, 0, keyAddr, keyStr)
	writeStringHeader(labelArray, 16, valAddr, valStr)

	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{PtrSize: 8, GOARCH: "amd64"},
		Objects: []heapsnapshot.Object{
			{Addr: gAddr, Contents: gObj},
			{Addr: labelMapAddr, Contents: labelMap},
			{Addr: labelArrayAddr, Contents: labelArray},
		},
		Goroutines: []heapsnapshot.Goroutine{{ID: 1, Addr: gAddr}},
	}
	layout := mustManualLayout(t, 0x18)

	t.Run("without_extra_memory_string_missing", func(t *testing.T) {
		res := DecodeAll(snap, layout, Options{})
		if res.Stats.GoroutinesFailed != 1 {
			t.Fatalf("expected 1 failed goroutine, got stats %+v", res.Stats)
		}
		if res.Stats.StringsMissing != 1 {
			t.Fatalf("expected StringsMissing=1, got %+v", res.Stats)
		}
		if res.Goroutines[0].Status != StatusStringMissing {
			t.Fatalf("status = %s err = %s", res.Goroutines[0].Status, res.Goroutines[0].Error)
		}
	})

	t.Run("with_extra_memory_decodes", func(t *testing.T) {
		// Lay key bytes at keyAddr and value bytes at valAddr in one
		// contiguous extra-memory range, padded with zeros so the
		// fixed offsets remain stable.
		buf := make([]byte, 0x200)
		copy(buf[0:], []byte(keyStr))
		copy(buf[0x100:], []byte(valStr))
		extra := &extraReader{base: keyAddr, data: buf}

		res := DecodeAll(snap, layout, Options{ExtraMemory: extra})
		if res.Stats.GoroutinesDecoded != 1 {
			t.Fatalf("expected 1 decoded goroutine, got stats %+v\nerr=%s", res.Stats, res.Goroutines[0].Error)
		}
		if got := res.LabelsByGID[1][keyStr]; got != valStr {
			t.Fatalf("labels = %#v", res.LabelsByGID[1])
		}
	})

	t.Run("with_extra_memory_implements_NamedReader_via_adapter", func(t *testing.T) {
		// Even when the supplied reader does NOT implement
		// addrspace.NamedReader, the decoder must adapt it transparently.
		buf := make([]byte, 0x200)
		copy(buf[0:], []byte(keyStr))
		copy(buf[0x100:], []byte(valStr))
		var bare addrspace.Reader = &unnamedExtraReader{e: &extraReader{base: keyAddr, data: buf}}
		res := DecodeAll(snap, layout, Options{ExtraMemory: bare})
		if res.Stats.GoroutinesDecoded != 1 {
			t.Fatalf("expected 1 decoded goroutine, got %+v", res.Stats)
		}
	})
}

type unnamedExtraReader struct{ e *extraReader }

func (u *unnamedExtraReader) ReadAtAddr(addr, size uint64) ([]byte, bool) {
	return u.e.ReadAtAddr(addr, size)
}
