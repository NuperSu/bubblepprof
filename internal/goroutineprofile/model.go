// Package goroutineprofile parses the goroutine.pprof entry embedded in
// snapshot.tar and exposes a small, label-focused model.
//
// goroutine.pprof is the gzipped protobuf profile that
// runtime/pprof.Lookup("goroutine") writes when debug=0. We do not need
// every detail of that profile to recover pprof labels — only label sets,
// stack traces, and sample counts. The minimal model below keeps the
// downstream correlation code independent of github.com/google/pprof.
package goroutineprofile

// Profile is the parsed view of one goroutine.pprof payload.
type Profile struct {
	Goroutines []GoroutineSample
	Warnings   []string
}

// GoroutineSample is one sample inside the goroutine profile. The Go
// runtime emits one sample per (stack, labels) tuple; Count tells how
// many goroutines share that tuple at the moment of capture.
type GoroutineSample struct {
	Labels  map[string]string
	Numeric map[string]int64
	Frames  []Frame
	Count   int64
}

// Frame is one stack frame within a sample. The function name is the
// only field that the heap-side stack signature uses today, but File and
// Line are surfaced for diagnostics.
type Frame struct {
	Func string
	File string
	Line int64
}
