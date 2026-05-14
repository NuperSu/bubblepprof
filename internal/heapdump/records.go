package heapdump

// Header is the heap dump file header emitted by runtime.WriteHeapDump.
// The trailing newline is part of the marker.
const Header = "go1.7 heap dump"

// Record tags as written by runtime/heapdump.go.
const (
	tagEOF             uint64 = 0
	tagObject          uint64 = 1
	tagOtherRoot       uint64 = 2
	tagType            uint64 = 3
	tagGoroutine       uint64 = 4
	tagStackFrame      uint64 = 5
	tagParams          uint64 = 6
	tagFinalizer       uint64 = 7
	tagItab            uint64 = 8
	tagOSThread        uint64 = 9
	tagMemStats        uint64 = 10
	tagQueuedFinalizer uint64 = 11
	tagData            uint64 = 12
	tagBSS             uint64 = 13
	tagDefer           uint64 = 14
	tagPanic           uint64 = 15
	tagMemProf         uint64 = 16
	tagAllocSample     uint64 = 17
)
