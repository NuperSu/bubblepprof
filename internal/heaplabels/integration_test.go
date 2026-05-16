package heaplabels

import (
	"context"
	"fmt"
	"runtime"
	"runtime/pprof"
	"slices"
	"testing"

	"bubblepprof/internal/capture"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/runtimelayout"
)

func TestRuntimeHeapDumpDynamicPprofLabels(t *testing.T) {
	labels, res := captureRuntimePprofLabels(t,
		runtimeWorkerSpec{
			Labels: dynamicKV("bubble", "alpha", "job", "42"),
			Want:   map[string]string{"bubble": "alpha"},
		},
	)

	got := findLabels(labels, map[string]string{"bubble": "alpha"})
	if got == nil {
		t.Fatalf("no goroutine recovered bubble=alpha; stats=%+v warnings=%v", res.Stats, res.Warnings)
	}
	if got["job"] != "42" {
		t.Fatalf("recovered bubble=alpha but missing job=42: %#v", got)
	}
}

func TestRuntimeHeapDumpLiteralPprofLabels(t *testing.T) {
	labels, res := captureRuntimePprofLabels(t,
		runtimeWorkerSpec{
			Labels: []string{"bubble", "alpha", "job", "42"},
			Want:   map[string]string{"bubble": "alpha"},
		},
	)

	got := findLabels(labels, map[string]string{"bubble": "alpha"})
	if got != nil {
		if got["job"] != "42" {
			t.Fatalf("literal labels recovered bubble=alpha but missing job=42: %#v", got)
		}
		return
	}

	if res.Stats.StringsMissing == 0 && !hasStatus(res, StatusStringMissing) {
		t.Fatalf("literal labels were not recovered, but no string-missing diagnostic was reported; stats=%+v results=%#v",
			res.Stats, res.Goroutines)
	}
	t.Logf("literal pprof labels were not recovered from heap object bytes; heap-native recovery should fall back when label string bytes are absent")
}

func TestRuntimeHeapDumpNestedOverridePprofLabels(t *testing.T) {
	labels, res := captureRuntimePprofLabels(t,
		runtimeWorkerSpec{
			OuterLabels: dynamicKV("bubble", "outer", "tenant", "acme"),
			Labels:      dynamicKV("bubble", "inner", "job", "42"),
			Want:        map[string]string{"bubble": "inner"},
		},
	)

	got := findLabels(labels, map[string]string{"bubble": "inner"})
	if got == nil {
		t.Fatalf("no goroutine recovered nested bubble=inner; stats=%+v warnings=%v labels=%#v",
			res.Stats, res.Warnings, labels)
	}
	want := map[string]string{"bubble": "inner", "tenant": "acme", "job": "42"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("nested labels[%s] = %q, want %q; all labels=%#v", k, got[k], v, got)
		}
	}
}

func TestRuntimeHeapDumpDuplicateKeyOverridePprofLabels(t *testing.T) {
	labels, res := captureRuntimePprofLabels(t,
		runtimeWorkerSpec{
			Labels: dynamicKV("bubble", "outer", "bubble", "inner", "job", "42"),
			Want:   map[string]string{"bubble": "inner"},
		},
	)

	got := findLabels(labels, map[string]string{"bubble": "inner"})
	if got == nil {
		t.Fatalf("no goroutine recovered duplicate-key override bubble=inner; stats=%+v labels=%#v",
			res.Stats, labels)
	}
	if got["bubble"] != "inner" || got["job"] != "42" {
		t.Fatalf("duplicate-key labels = %#v", got)
	}
	if got["bubble"] == "outer" {
		t.Fatalf("duplicate-key override kept old value: %#v", got)
	}
}

type runtimeWorkerSpec struct {
	OuterLabels []string
	Labels      []string
	Want        map[string]string
}

func captureRuntimePprofLabels(t *testing.T, specs ...runtimeWorkerSpec) (map[uint64]map[string]string, Result) {
	t.Helper()

	stop := make(chan struct{})
	defer close(stop)

	for i, spec := range specs {
		startRuntimePprofWorker(t, stop, i, spec)
	}

	captured, err := capture.Capture(context.Background(), capture.CaptureOptions{GCBeforeHeapDump: true})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	defer captured.Cleanup()

	snap, err := heapdump.Parse(captured.HeapDump, heapdump.Options{KeepObjectContents: true})
	if err != nil {
		t.Fatalf("parse heap dump: %v", err)
	}

	layout, haveLayout := LookupLayout(snap)
	if !haveLayout {
		for _, spec := range specs {
			if len(spec.Want) == 0 {
				continue
			}
			candidates := FindOffsetCandidates(snap, NewMemory(snap), spec.Want, Options{})
			if len(candidates) == 1 {
				manual, err := runtimelayout.Manual(LookupInputFromSnapshot(snap), candidates[0].Offset)
				if err != nil {
					t.Fatalf("runtimelayout.Manual: %v", err)
				}
				layout, haveLayout = manual, true
				break
			}
			if len(candidates) > 1 {
				t.Fatalf("ambiguous offset candidates for %v: %#v", spec.Want, candidates)
			}
		}
	}
	if !haveLayout {
		t.Skipf("no runtime.g.labels offset found for %s %s", runtime.Version(), runtime.GOARCH)
	}

	res := DecodeAll(snap, layout, Options{})
	return res.LabelsByGID, res
}

func startRuntimePprofWorker(t *testing.T, stop <-chan struct{}, idx int, spec runtimeWorkerSpec) {
	t.Helper()
	ready := make(chan struct{})
	start := func(ctx context.Context) {
		go func() {
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, 1<<20)
			close(ready)
			<-stop
			runtime.KeepAlive(data)
		}()
		<-ready
	}
	if len(spec.OuterLabels) > 0 {
		pprof.Do(context.Background(), pprof.Labels(spec.OuterLabels...), func(ctx context.Context) {
			pprof.Do(ctx, pprof.Labels(spec.Labels...), start)
		})
		return
	}
	if len(spec.Labels) == 0 {
		t.Fatalf("worker %d has no labels", idx)
	}
	pprof.Do(context.Background(), pprof.Labels(spec.Labels...), start)
}

func findLabels(labelsByGID map[uint64]map[string]string, want map[string]string) map[string]string {
	gids := make([]uint64, 0, len(labelsByGID))
	for gid := range labelsByGID {
		gids = append(gids, gid)
	}
	slices.Sort(gids)
	for _, gid := range gids {
		labels := labelsByGID[gid]
		if labelsContain(labels, want) {
			return labels
		}
	}
	return nil
}

func labelsContain(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func hasStatus(res Result, status DecodeStatus) bool {
	for _, gr := range res.Goroutines {
		if gr.Status == status {
			return true
		}
	}
	return false
}

func dynamicKV(kv ...string) []string {
	out := make([]string, len(kv))
	for i, s := range kv {
		out[i] = dynamicString(s)
	}
	return out
}

func TestLookupLayoutVerifiedGo126AMD64(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Params: heapsnapshot.DumpParams{
			PtrSize:      8,
			GOARCH:       "amd64",
			BuildVersion: "go1.26.3-X:nodwarf5",
		},
	}
	layout, ok := LookupLayout(snap)
	if !ok || layout.GLabelsOffset != 0x160 {
		t.Fatalf("LookupLayout offset = %#x, ok=%t", layout.GLabelsOffset, ok)
	}
	if layout.Source != runtimelayout.SourceTable {
		t.Fatalf("Source = %q", layout.Source)
	}
}

func dynamicString(s string) string {
	out := fmt.Sprintf("%s", string(append([]byte(nil), s...)))
	retainedDynamicStrings = append(retainedDynamicStrings, out)
	return out
}

var retainedDynamicStrings []string
