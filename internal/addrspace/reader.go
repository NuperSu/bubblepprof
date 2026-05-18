// Package addrspace reads bytes at runtime virtual addresses for the
// heap-native pprof label decoder. It recovers ordinary runtime/pprof
// label string bytes that are not stored in heap dump object contents
// (typical for pprof.Labels("job", "42") where the string headers point
// into the executable's read-only data segment).
//
// A Reader abstracts "give me size bytes starting at this virtual
// address". Concrete implementations:
//
//	*RangeReader     — backed by an in-memory slice of byte ranges,
//	                   used to expose heap dump object contents.
//	*ProcessReader   — reads the running process address space:
//	                     Linux / FreeBSD: read-only mappings via procfs
//	                       (/proc/self/mem); FreeBSD falls back to ELF
//	                       rodata when procfs is not mounted.
//	                     macOS: ASLR-corrected Mach-O executable sections.
//	                     Windows: ASLR-corrected PE executable sections.
//	                   Used by /debug/memusage.
//	*ELFReader       — reads PT_LOAD segments from an ELF executable
//	                   on disk (offline fallback; non-PIE only).
//	Composite        — tries readers in order, returning the first hit.
//
// Readers never panic on malformed addresses, never silently truncate,
// and reject overflowing reads. They return a fresh slice on success so
// callers may keep the bytes after the reader is closed.
package addrspace

import (
	"encoding/binary"
	"errors"
)

// ErrUnsupported is returned by constructors that have no implementation
// for the current platform. Callers should treat it as a soft failure
// and continue without the reader.
var ErrUnsupported = errors.New("addrspace: unsupported on this platform")

// Reader reads bytes at a runtime virtual address.
//
// Implementations MUST:
//
//   - Return ok=true and an empty slice when size == 0.
//   - Return ok=false when addr == 0 and size > 0.
//   - Return ok=false when addr + size overflows uint64.
//   - Never panic; return ok=false for any unreadable range.
//   - Return a fresh copy or a read-only view; callers MUST NOT modify
//     the returned bytes, and may retain them after the call.
type Reader interface {
	ReadAtAddr(addr uint64, size uint64) ([]byte, bool)
}

// NamedReader is a Reader that knows what to call itself in diagnostics
// (e.g. "heap", "process", "elf:./server"). Composite.SourceFor reports
// the first reader that satisfies a given read.
type NamedReader interface {
	Reader
	Name() string
}

// ReadString reads a (possibly non-UTF-8) Go string of length bytes
// starting at addr. length==0 always succeeds with the empty string.
// length>max fails to bound runaway reads.
func ReadString(r Reader, addr uint64, length uint64, max uint64) (string, bool) {
	if length == 0 {
		return "", true
	}
	if max > 0 && length > max {
		return "", false
	}
	if r == nil {
		return "", false
	}
	b, ok := r.ReadAtAddr(addr, length)
	if !ok {
		return "", false
	}
	return string(b), true
}

// ReadUintptr reads a ptrSize-wide little-/big-endian unsigned integer
// at addr. ptrSize must be 4 or 8.
func ReadUintptr(r Reader, addr uint64, ptrSize int, order binary.ByteOrder) (uint64, bool) {
	if r == nil || order == nil {
		return 0, false
	}
	if ptrSize != 4 && ptrSize != 8 {
		return 0, false
	}
	b, ok := r.ReadAtAddr(addr, uint64(ptrSize))
	if !ok {
		return 0, false
	}
	switch ptrSize {
	case 4:
		return uint64(order.Uint32(b)), true
	case 8:
		return order.Uint64(b), true
	}
	return 0, false
}

// AddUint64 returns a+b and reports false on uint64 wrap-around.
func AddUint64(a, b uint64) (uint64, bool) {
	c := a + b
	return c, c >= a
}
