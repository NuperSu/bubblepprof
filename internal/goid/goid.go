// Package goid exposes a prototype-only helper that recovers the current
// goroutine's runtime-assigned ID by parsing the header of runtime.Stack.
//
// Go does not export a goroutine ID API. The heap dump emitted by
// runtime/debug.WriteHeapDump records goroutine IDs assigned by the
// runtime, and matching live goroutines to their dumped record is the
// only reliable way to attach pprof labels to heap goroutine records.
//
// This helper is for the prototype label-registry path only. It is not a
// stable API.
package goid

import (
	"runtime"
)

// CurrentGoroutineID returns the runtime ID of the calling goroutine.
//
// It parses the standard header that runtime.Stack writes:
//
//	goroutine 123 [running]:
//
// The helper never panics; if the buffer is too small or the header is
// unrecognized, it returns (0, false).
func CurrentGoroutineID() (uint64, bool) {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	return ParseHeader(buf[:n])
}

// ParseHeader parses a runtime.Stack header prefix and returns the
// goroutine ID encoded in it. It returns (0, false) if the buffer does
// not start with a recognizable "goroutine <decimal-int> [" header.
//
// Exposed so tests can validate the parser without depending on the
// runtime.Stack format of the current process.
func ParseHeader(b []byte) (uint64, bool) {
	const prefix = "goroutine "
	if len(b) < len(prefix) {
		return 0, false
	}
	for i := 0; i < len(prefix); i++ {
		if b[i] != prefix[i] {
			return 0, false
		}
	}
	i := len(prefix)
	if i >= len(b) || b[i] < '0' || b[i] > '9' {
		return 0, false
	}
	var id uint64
	for ; i < len(b); i++ {
		c := b[i]
		if c < '0' || c > '9' {
			break
		}
		// Detect overflow conservatively. Goroutine IDs are uint64; the
		// runtime would not realistically reach 2^64, but parse defensively.
		next := id*10 + uint64(c-'0')
		if next < id {
			return 0, false
		}
		id = next
	}
	// Header must continue with " [".
	if i >= len(b) || b[i] != ' ' {
		return 0, false
	}
	if i+1 >= len(b) || b[i+1] != '[' {
		return 0, false
	}
	return id, true
}
