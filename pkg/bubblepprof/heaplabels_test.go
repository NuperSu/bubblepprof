package bubblepprof

import (
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"unsafe"

	"github.com/NuperSu/bubblepprof/internal/capture"
	"github.com/NuperSu/bubblepprof/internal/heapdump"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
)

// runtimeIsClaimedSupported reports whether the current runtime's (version,
// arch, ptrSize, endian) tuple is present in the verified-layout table.
// It uses runtime.Version() and unsafe.Sizeof directly — not snapshot-derived
// values — so a snapshot-parsing bug or table-lookup regression on a
// known-good platform surfaces as Fatalf rather than a silent Skip.
func runtimeIsClaimedSupported() bool {
	ptrSize := int(unsafe.Sizeof(uintptr(0)))
	var probe uint16 = 0x0102
	bigEndian := *(*byte)(unsafe.Pointer(&probe)) == 0x01
	_, ok := runtimelayout.Lookup(runtimelayout.LookupInput{
		GoVersion: runtime.Version(),
		GOARCH:    runtime.GOARCH,
		PtrSize:   ptrSize,
		BigEndian: bigEndian,
	})
	return ok
}

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
		if runtimeIsClaimedSupported() {
			t.Fatalf("claimed-supported runtime %s/%s has no layout entry: snapshot params goarch=%s ptrSize=%d bigEndian=%v",
				runtime.Version(), runtime.GOARCH,
				snap.Params.GOARCH, snap.Params.PtrSize, snap.Params.BigEndian)
		}
		t.Skipf("no verified runtime.g.labels layout for %s/%s", runtime.Version(), runtime.GOARCH)
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
