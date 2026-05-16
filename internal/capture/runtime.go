package capture

import "runtime/debug"

// RuntimeHeapDumpWriter is the production HeapDumpWriter: it calls
// debug.WriteHeapDump with the supplied file descriptor.
type RuntimeHeapDumpWriter struct{}

func (RuntimeHeapDumpWriter) WriteHeapDump(fd uintptr) error {
	debug.WriteHeapDump(fd)
	return nil
}
