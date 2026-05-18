package memusage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bubblepprof/internal/addrspace"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/heapsnapshot"
)

// fakeCapturer writes a single byte to a temp file so subsequent
// heapdump.Parse fails immediately. It records whether CaptureHeapDump
// was called and supports preempting the test via a sentinel error.
type fakeCapturer struct {
	dir   string
	gcArg bool
	err   error
}

func (f *fakeCapturer) CaptureHeapDump(ctx context.Context, gcBefore bool) (string, func(), error) {
	f.gcArg = gcBefore
	if f.err != nil {
		return "", nil, f.err
	}
	if f.dir == "" {
		f.dir = os.TempDir()
	}
	path := filepath.Join(f.dir, "fake-heap.dump")
	if err := os.WriteFile(path, []byte("BAD"), 0o600); err != nil {
		return "", nil, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

type fakeRecoverer struct {
	result heaplabels.Result
	err    error
}

func (f fakeRecoverer) Recover(snap *heapsnapshot.HeapSnapshot, extra addrspace.Reader) (heaplabels.Result, error) {
	return f.result, f.err
}

func TestComputer_CtxCancelledBeforeCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Computer{
		Capturer:  &fakeCapturer{},
		Recoverer: fakeRecoverer{},
		Opts:      Options{},
	}
	_, err := c.Compute(ctx, Request{Labels: map[string]string{"a": "b"}})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled wrapped", err)
	}
}

func TestComputer_CapturerForwardsGCFlag(t *testing.T) {
	for _, gc := range []bool{true, false} {
		t.Run(name(gc), func(t *testing.T) {
			fc := &fakeCapturer{err: errors.New("stop after capture")}
			c := &Computer{
				Capturer:  fc,
				Recoverer: fakeRecoverer{},
				Opts:      Options{GCBeforeHeapDump: gc},
			}
			_, _ = c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
			if fc.gcArg != gc {
				t.Fatalf("capturer.gcArg = %t, want %t", fc.gcArg, gc)
			}
		})
	}
}

func name(b bool) string {
	if b {
		return "gc"
	}
	return "no-gc"
}

// --- dump helpers ---

func putUvarint(w *bytes.Buffer, x uint64) {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], x)
	w.Write(b[:n])
}

func putString(w *bytes.Buffer, s string) {
	putUvarint(w, uint64(len(s)))
	w.WriteString(s)
}

// minimalValidDump returns the bytes of a parseable heap dump with no
// objects or goroutines. The params record uses 8-byte pointers and little
// endian — sufficient for runtimelayout.Lookup to match.
func minimalValidDump(arch, version string) []byte {
	var buf bytes.Buffer
	buf.WriteString("go1.7 heap dump\n")
	putUvarint(&buf, 6)           // tagParams
	putUvarint(&buf, 0)           // bigEndian=false
	putUvarint(&buf, 8)           // ptrSize=8
	putUvarint(&buf, 0)           // heapStart
	putUvarint(&buf, 0)           // heapEnd
	putString(&buf, arch)         // goarch
	putString(&buf, version)      // buildVersion
	putUvarint(&buf, 1)           // numCPU
	putUvarint(&buf, 0)           // tagEOF
	return buf.Bytes()
}

// dumpCapturer writes a valid minimal heap dump to a temp file and returns
// its path, satisfying HeapDumpCapturer.
type dumpCapturer struct {
	arch    string
	version string
}

func (d dumpCapturer) CaptureHeapDump(_ context.Context, _ bool) (string, func(), error) {
	f, err := os.CreateTemp("", "memusage-test-*.heap")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	cleanup := func() { os.Remove(name) }
	f.Write(minimalValidDump(d.arch, d.version))
	f.Close()
	return name, cleanup, nil
}

// cancelCapture cancels ctx inside CaptureHeapDump so the ctx.Err() check
// that follows the capture call in Computer.Compute fires.
type cancelCapture struct {
	cancel   context.CancelFunc
	delegate HeapDumpCapturer
}

func (c *cancelCapture) CaptureHeapDump(ctx context.Context, gcBefore bool) (string, func(), error) {
	path, cleanup, err := c.delegate.CaptureHeapDump(ctx, gcBefore)
	c.cancel()
	return path, cleanup, err
}

// --- NewComputer ---

func TestNewComputer(t *testing.T) {
	c := NewComputer(Options{})
	if c == nil {
		t.Fatal("NewComputer returned nil")
	}
	if _, ok := c.Capturer.(RuntimeHeapDumpCapturer); !ok {
		t.Fatalf("Capturer type = %T, want RuntimeHeapDumpCapturer", c.Capturer)
	}
	if _, ok := c.Recoverer.(DefaultLabelRecoverer); !ok {
		t.Fatalf("Recoverer type = %T, want DefaultLabelRecoverer", c.Recoverer)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- Computer.Close ---

func TestComputer_Close_NilReceiver(t *testing.T) {
	var c *Computer
	if err := c.Close(); err != nil {
		t.Fatalf("(*Computer)(nil).Close() = %v, want nil", err)
	}
}

func TestComputer_Close_NilProcReader(t *testing.T) {
	c := &Computer{}
	if err := c.Close(); err != nil {
		t.Fatalf("Close with nil procReader = %v, want nil", err)
	}
}

func TestComputer_Close_WithProcReader(t *testing.T) {
	r, _ := openProcessReader(false)
	if r == nil {
		t.Skip("process reader not available on this platform")
	}
	c := &Computer{procReader: r}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// nthCallContext is a context.Context that returns a cancelError on the Nth
// call to Err(), allowing tests to cancel a context mid-flight.
type nthCallContext struct {
	context.Context
	mu      sync.Mutex
	call    int
	failAt  int
}

func (c *nthCallContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call++
	if c.call >= c.failAt {
		return context.Canceled
	}
	return nil
}

func (c *nthCallContext) Done() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			c.mu.Lock()
			if c.call >= c.failAt {
				c.mu.Unlock()
				close(ch)
				return
			}
			c.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	return ch
}

// --- RuntimeHeapDumpCapturer.CaptureHeapDump ---

func TestRuntimeHeapDumpCapturer_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := RuntimeHeapDumpCapturer{}.CaptureHeapDump(ctx, false)
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestRuntimeHeapDumpCapturer_CtxCancelledAfterGC verifies the ctx.Err() check
// that fires after GC but before debug.WriteHeapDump. The nthCallContext returns
// nil on the first Err() call (before CreateTemp) and Canceled on the second
// (after GC).
func TestRuntimeHeapDumpCapturer_CtxCancelledAfterGC(t *testing.T) {
	ctx := &nthCallContext{
		Context: context.Background(),
		failAt:  2,
	}
	_, _, err := RuntimeHeapDumpCapturer{}.CaptureHeapDump(ctx, true)
	if err == nil {
		t.Fatal("expected error from context cancelled after GC, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRuntimeHeapDumpCapturer_GCBeforeCapture(t *testing.T) {
	// gcBefore=true must trigger runtime.GC() and still produce a valid dump.
	path, cleanup, err := RuntimeHeapDumpCapturer{}.CaptureHeapDump(context.Background(), true)
	if err != nil {
		t.Fatalf("CaptureHeapDump(gcBefore=true): %v", err)
	}
	defer cleanup()
	if path == "" {
		t.Fatal("path is empty")
	}
}

func TestRuntimeHeapDumpCapturer_Happy(t *testing.T) {
	path, cleanup, err := RuntimeHeapDumpCapturer{}.CaptureHeapDump(context.Background(), false)
	if err != nil {
		t.Fatalf("CaptureHeapDump: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	defer cleanup()
	if path == "" {
		t.Fatal("path is empty")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if fi.Size() == 0 {
		t.Fatal("heap dump file is empty")
	}
}

// --- DefaultLabelRecoverer.Recover ---

func TestDefaultLabelRecoverer_NilSnap(t *testing.T) {
	r := DefaultLabelRecoverer{}
	_, err := r.Recover(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil snapshot, got nil")
	}
}

func TestDefaultLabelRecoverer_SupportedLayout(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			GOARCH:       "amd64",
			PtrSize:      8,
			BuildVersion: "go1.26.0",
		},
	}
	r := DefaultLabelRecoverer{}
	res, err := r.Recover(snap, nil)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if res.Stats.GoroutinesUnsupported != 0 {
		t.Fatalf("GoroutinesUnsupported = %d, want 0", res.Stats.GoroutinesUnsupported)
	}
}

func TestDefaultLabelRecoverer_UnsupportedLayout(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			GOARCH:       "s390x",
			PtrSize:      8,
			BuildVersion: "go1.99.0",
		},
	}
	r := DefaultLabelRecoverer{}
	res, err := r.Recover(snap, nil)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected unsupported-runtime warning, got none")
	}
}

// --- openProcessReader ---

func TestOpenProcessReader_Disabled(t *testing.T) {
	r, warn := openProcessReader(true)
	if r != nil {
		r.Close()
		t.Fatalf("expected nil reader for disabled=true, got %T", r)
	}
	if warn == "" {
		t.Fatal("expected non-empty warning for disabled reader")
	}
}

func TestOpenProcessReader_Available(t *testing.T) {
	r, warn := openProcessReader(false)
	if r != nil {
		defer r.Close()
		if warn != "" {
			t.Fatalf("reader is non-nil but warning = %q", warn)
		}
	} else {
		if warn == "" {
			t.Fatal("nil reader with empty warning: expected a diagnostic message")
		}
	}
}

// --- Computer.Compute ---

// badPathCapturer returns a path that does not exist so os.Open in Compute fails.
type badPathCapturer struct{}

func (badPathCapturer) CaptureHeapDump(_ context.Context, _ bool) (string, func(), error) {
	return "/nonexistent-memusage-test-path", func() {}, nil
}

// cancelAfterParse wraps a recoverer and cancels the context before returning,
// exercising the ctx.Err() check after label recovery in Computer.Compute.
type cancelAfterParse struct {
	cancel   context.CancelFunc
	delegate LabelRecoverer
}

func (c *cancelAfterParse) Recover(snap *heapsnapshot.HeapSnapshot, extra addrspace.Reader) (heaplabels.Result, error) {
	res, err := c.delegate.Recover(snap, extra)
	c.cancel()
	return res, err
}

func TestComputer_Compute_NilReceiver(t *testing.T) {
	var c *Computer
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	if err == nil {
		t.Fatal("expected error from nil Computer, got nil")
	}
}

func TestComputer_Compute_NilCapturer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Computer{
		Capturer:  nil, // falls back to RuntimeHeapDumpCapturer
		Recoverer: fakeRecoverer{},
	}
	_, err := c.Compute(ctx, Request{Labels: map[string]string{"a": "b"}})
	var cfe *CaptureFailedError
	if !errors.As(err, &cfe) {
		t.Fatalf("expected CaptureFailedError, got %v", err)
	}
}

func TestComputer_Compute_NilRecoverer(t *testing.T) {
	c := &Computer{
		Capturer:  dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: nil, // falls back to DefaultLabelRecoverer{}
		Opts:      Options{DisableProcessMemoryReader: true},
	}
	resp, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	if err != nil {
		t.Fatalf("Compute with nil Recoverer: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestComputer_Compute_OpenFileFails(t *testing.T) {
	c := &Computer{
		Capturer:  badPathCapturer{},
		Recoverer: fakeRecoverer{},
	}
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	var cfe *CaptureFailedError
	if !errors.As(err, &cfe) {
		t.Fatalf("expected CaptureFailedError, got %v", err)
	}
}

func TestComputer_Compute_CtxCancelledAfterParse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Computer{
		Capturer: dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: &cancelAfterParse{
			cancel:   cancel,
			delegate: fakeRecoverer{},
		},
		Opts: Options{DisableProcessMemoryReader: true},
	}
	_, err := c.Compute(ctx, Request{Labels: map[string]string{"a": "b"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestComputer_Compute_ParseFails(t *testing.T) {
	c := &Computer{
		Capturer:  &fakeCapturer{},
		Recoverer: fakeRecoverer{},
	}
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	var pfe *ParseFailedError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected ParseFailedError, got %v", err)
	}
}

func TestComputer_Compute_RecovererError(t *testing.T) {
	recErr := errors.New("decode failed")
	c := &Computer{
		Capturer:  dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: fakeRecoverer{err: recErr},
		Opts:      Options{DisableProcessMemoryReader: true},
	}
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "decode failed") {
		t.Fatalf("error = %v, want to contain 'decode failed'", err)
	}
}

func TestComputer_Compute_CtxCancelledAfterCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Computer{
		Capturer: &cancelCapture{
			cancel:   cancel,
			delegate: dumpCapturer{arch: "amd64", version: "go1.26.0"},
		},
		Recoverer: fakeRecoverer{},
		Opts:      Options{DisableProcessMemoryReader: true},
	}
	_, err := c.Compute(ctx, Request{Labels: map[string]string{"a": "b"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestComputer_Compute_UnsupportedRuntime(t *testing.T) {
	c := &Computer{
		Capturer: dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: fakeRecoverer{result: heaplabels.Result{
			LabelsByGID: map[uint64]map[string]string{},
			Stats: heaplabels.Stats{
				GoroutinesTotal:       1,
				GoroutinesUnsupported: 1,
			},
		}},
		Opts: Options{DisableProcessMemoryReader: true},
	}
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	var ure *UnsupportedRuntimeError
	if !errors.As(err, &ure) {
		t.Fatalf("expected UnsupportedRuntimeError, got %v", err)
	}
}

func TestComputer_Compute_WithProcReader(t *testing.T) {
	// When DisableProcessMemoryReader=false, processReader opens /proc/self/mem
	// (Linux) and passes it as extra to the recoverer.  On other platforms
	// the reader is unavailable but the path should still not error.
	c := &Computer{
		Capturer: dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: fakeRecoverer{result: heaplabels.Result{
			LabelsByGID: map[uint64]map[string]string{},
		}},
		Opts: Options{DisableProcessMemoryReader: false},
	}
	defer c.Close()
	resp, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	if err != nil {
		t.Fatalf("Compute with proc reader: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestComputer_Compute_ProcWarningAppearsInStringMissingError(t *testing.T) {
	// When the process memory reader is disabled and label decoding reports
	// string_missing failures, the proc-reader warning must appear in the
	// StringMissingError so error responses can surface it.
	c := &Computer{
		Capturer: dumpCapturer{arch: "amd64", version: "go1.26.0"},
		Recoverer: fakeRecoverer{result: heaplabels.Result{
			LabelsByGID: map[uint64]map[string]string{},
			Stats: heaplabels.Stats{
				GoroutinesTotal:  1,
				GoroutinesFailed: 1,
				StringsMissing:   1,
			},
		}},
		Opts: Options{DisableProcessMemoryReader: true},
	}
	_, err := c.Compute(context.Background(), Request{Labels: map[string]string{"a": "b"}})
	if err == nil {
		t.Fatal("expected StringMissingError, got nil")
	}
	var sme *StringMissingError
	if !errors.As(err, &sme) {
		t.Fatalf("error = %v, want StringMissingError", err)
	}
	found := false
	for _, w := range sme.Warnings {
		if strings.Contains(w, "process memory reader") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected proc-reader warning in StringMissingError.Warnings, got %v", sme.Warnings)
	}
}
