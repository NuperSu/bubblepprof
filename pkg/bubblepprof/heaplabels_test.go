package bubblepprof

import (
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/runtimelayout"
)

func TestHeapNativeLabelRecovery(t *testing.T) {
	ready := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)

	// Use heap-allocated label strings so bytes are in the heap dump object
	// contents and recoverable without the process memory reader.
	ctx := pprof.WithLabels(context.Background(), pprof.Labels(
		strings.Clone("bubble"), strings.Clone("alpha"),
		strings.Clone("job"), strings.Clone("42"),
	))
	go func() {
		pprof.SetGoroutineLabels(ctx)
		data := make([]byte, 1<<20)
		close(ready)
		<-stop
		runtime.KeepAlive(data)
	}()
	<-ready

	captured, err := capture.Capture(context.Background(), capture.CaptureOptions{GCBeforeHeapDump: true})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer captured.Cleanup()

	snap, err := heapdump.Parse(captured.HeapDump, heapdump.Options{KeepObjectContents: true, Strict: true})
	if err != nil {
		t.Fatalf("Parse heap dump: %v", err)
	}
	layout, ok := runtimelayout.Lookup(heaplabels.LookupInputFromSnapshot(snap))
	if !ok {
		t.Skipf("no verified runtime.g.labels layout for %s %s", runtime.Version(), runtime.GOARCH)
	}
	res := heaplabels.DecodeAll(snap, layout, heaplabels.Options{})
	for _, labels := range res.LabelsByGID {
		if labels["bubble"] == "alpha" {
			if labels["job"] != "42" {
				t.Fatalf("recovered bubble=alpha but job=%q; labels=%#v", labels["job"], labels)
			}
			return
		}
	}
	t.Fatalf("pprof labels were not recovered from heap dump; stats=%+v warnings=%v",
		res.Stats, res.Warnings)
}
