package memusage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

func (f fakeRecoverer) Recover(snap *heapsnapshot.HeapSnapshot) (heaplabels.Result, error) {
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
