package runtimelayout

import (
	"encoding/binary"
	"runtime"
	"unsafe"
)

// LocalInput returns the LookupInput describing the currently executing
// process runtime. The in-process /debug/memusage handler uses it to
// refuse unsupported runtimes before paying for a stop-the-world heap
// dump; the parsed dump's params remain the authoritative lookup key
// after parsing.
func LocalInput() LookupInput {
	return LookupInput{
		GoVersion: runtime.Version(),
		GOARCH:    runtime.GOARCH,
		PtrSize:   int(unsafe.Sizeof(uintptr(0))),
		BigEndian: localBigEndian(),
	}
}

func localBigEndian() bool {
	var buf [2]byte
	binary.NativeEndian.PutUint16(buf[:], 1)
	return buf[0] == 0
}
