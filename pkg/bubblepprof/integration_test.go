package bubblepprof

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/snapshot"
	"bubblepprof/internal/snapshotgraph"
	"bubblepprof/internal/snapshotparse"
)

func TestRuntimeSnapshotCaptureIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runtime heap dump integration test in short mode")
	}

	exe := filepath.Join(t.TempDir(), "snapshot-server")
	build := exec.Command("go", "build", "-o", exe, "./testdata/snapshot_server")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build snapshot server: %v\n%s", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, exe)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	addr, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read server address: %v; stderr=%s", err, stderr.String())
	}
	addr = strings.TrimSpace(addr)

	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://" + addr + snapshotPath)
	if err != nil {
		t.Fatalf("get snapshot: %v; stderr=%s", err, stderr.String())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s, stderr=%s", resp.StatusCode, body, stderr.String())
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	bundle, err := snapshot.ReadSnapshotBundle(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("read snapshot bundle: %v", err)
	}
	if len(bundle.HeapDump) == 0 {
		t.Fatal("heap.dump is empty")
	}
	if len(bundle.GoroutineProfile) == 0 {
		t.Fatal("goroutine.pprof is empty")
	}
	if bundle.Metadata.Format != snapshot.FormatV1 {
		t.Fatalf("format = %q", bundle.Metadata.Format)
	}

	// Phase 3: the heap dump must parse cleanly through the snapshotparse
	// pipeline and expose the broad invariants that distinguish a real
	// dump from an empty one.
	res, err := snapshotparse.ParseSnapshot(bytes.NewReader(body), heapdump.Options{})
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	snap := res.Snapshot
	if snap == nil {
		t.Fatal("parsed snapshot is nil")
	}
	if snap.Header != heapdump.Header {
		t.Fatalf("heap dump header = %q", snap.Header)
	}
	if snap.Params.PtrSize != 4 && snap.Params.PtrSize != 8 {
		t.Fatalf("ptr size = %d", snap.Params.PtrSize)
	}
	if snap.Stats.ObjectCount == 0 {
		t.Fatal("expected at least one object in real heap dump")
	}
	if snap.Stats.GoroutineCount == 0 {
		t.Fatal("expected at least one goroutine")
	}
	if snap.Stats.StackFrameCount == 0 {
		t.Fatal("expected at least one stack frame")
	}

	// Phase 4: building the snapshot graph must succeed and expose
	// non-degenerate counters. Exact reachability counts are runtime
	// version dependent and are not asserted.
	analysis, err := snapshotgraph.Build(snap, snapshotgraph.Options{})
	if err != nil {
		t.Fatalf("build snapshot graph: %v", err)
	}
	// Build is structural-only; the reach-derived counters this test
	// asserts on are filled by ComputeReachability.
	snapshotgraph.ComputeReachability(analysis)
	if analysis.Stats.Objects == 0 {
		t.Fatal("graph has no objects")
	}
	if analysis.Stats.Goroutines == 0 {
		t.Fatal("graph has no goroutines")
	}
	hasRootsOrFrames := false
	for _, gr := range analysis.Goroutines {
		if len(gr.Roots) > 0 {
			hasRootsOrFrames = true
			break
		}
	}
	if !hasRootsOrFrames {
		// fall back to checking the parsed snapshot, in case the runtime
		// emitted frames without typed pointer slots.
		for _, gr := range snap.Goroutines {
			if len(gr.Frames) > 0 {
				hasRootsOrFrames = true
				break
			}
		}
	}
	if !hasRootsOrFrames {
		t.Fatal("expected at least one goroutine with roots or frames")
	}
	if got, want := analysis.Stats.UnreachableObjects+analysis.Stats.GoroutineReachableObjects, analysis.Stats.Objects; got < want-analysis.Stats.GlobalReachableObjects {
		// sanity: unreachable + any reachable should account for all objects
		t.Fatalf("reachable accounting is degenerate: objects=%d unreachable=%d goroutine-reachable=%d global-reachable=%d",
			analysis.Stats.Objects,
			analysis.Stats.UnreachableObjects,
			analysis.Stats.GoroutineReachableObjects,
			analysis.Stats.GlobalReachableObjects)
	}
}
