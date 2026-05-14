package bubblepprof

import (
	"context"
	"runtime"
	"testing"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/runtimelayout"
)

func TestWrapperLiteralLabelsRecoverFromHeapDump(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("runtime.g.labels test currently targets amd64, got %s", runtime.GOARCH)
	}

	ready := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)

	Do(context.Background(), Labels("bubble", "alpha", "job", "42"), func(ctx context.Context) {
		Go(ctx, func(ctx context.Context) {
			data := make([]byte, 1<<20)
			close(ready)
			<-stop
			runtime.KeepAlive(data)
		})
		<-ready
	})

	captured, err := capture.Capture(context.Background(), capture.CaptureOptions{GCBeforeHeapDump: true})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer captured.Cleanup()

	snap, err := heapdump.Parse(captured.HeapDump, heapdump.Options{KeepObjectContents: true})
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
	t.Fatalf("wrapper literal labels were not recovered from heap dump; stats=%+v warnings=%v",
		res.Stats, res.Warnings)
}
