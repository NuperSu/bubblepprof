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

	"bubblepprof/internal/bubblereport"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/labelresolve"
	"bubblepprof/internal/snapshotgraph"
	"bubblepprof/internal/snapshotparse"
)

// TestBubbleAttributionIntegration brings up the example snapshot
// server, starts two labeled workers, captures a snapshot, and verifies
// the offline pipeline can recover both bubbles from labels.json.
func TestBubbleAttributionIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runtime bubble integration test in short mode")
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
	startWorker := func(bubble string) {
		resp, err := client.Get("http://" + addr + "/start-worker?bubble=" + bubble + "&mb=1")
		if err != nil {
			t.Fatalf("start-worker %s: %v", bubble, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("start-worker %s: status %d", bubble, resp.StatusCode)
		}
	}
	startWorker("alpha")
	startWorker("beta")

	resp, err := client.Get("http://" + addr + snapshotPath)
	if err != nil {
		t.Fatalf("get snapshot: %v; stderr=%s", err, stderr.String())
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}

	stopResp, err := client.Get("http://" + addr + "/stop-workers")
	if err == nil {
		_, _ = io.Copy(io.Discard, stopResp.Body)
		stopResp.Body.Close()
	}

	parsed, err := snapshotparse.ParseSnapshotForBubbles(bytes.NewReader(body), snapshotparse.BubbleParseOptions{
		HeapDump: heapdump.Options{},
	})
	if err != nil {
		t.Fatalf("ParseSnapshotForBubbles: %v", err)
	}
	if parsed.Labels == nil {
		t.Fatal("expected labels.json in snapshot")
	}
	if len(parsed.Labels.Goroutines) < 2 {
		t.Fatalf("expected >= 2 manifest entries, got %d", len(parsed.Labels.Goroutines))
	}

	var prof *goroutineprofile.Profile
	if len(parsed.GoroutineProfile) > 0 {
		prof, err = goroutineprofile.Parse(parsed.GoroutineProfile)
		if err != nil {
			t.Fatalf("parse goroutine profile: %v", err)
		}
	}
	resolution := labelresolve.ResolveLabels(parsed.Snapshot, parsed.Labels, prof, labelresolve.Options{})
	if resolution.MatchedFromManifest < 2 {
		t.Fatalf("MatchedFromManifest = %d, want >= 2", resolution.MatchedFromManifest)
	}

	analysis, err := snapshotgraph.Build(parsed.Snapshot, snapshotgraph.Options{})
	if err != nil {
		t.Fatalf("build snapshot graph: %v", err)
	}
	report, err := bubblereport.Build(bubblereport.Input{
		Analysis:    analysis,
		LabelsByGID: resolution.LabelsByGID,
	})
	if err != nil {
		t.Fatalf("build bubble report: %v", err)
	}
	var bubbleGroup *bubblereport.LabelGroup
	for i := range report.Groups {
		if report.Groups[i].Key == "bubble" {
			bubbleGroup = &report.Groups[i]
			break
		}
	}
	if bubbleGroup == nil {
		t.Fatalf("no 'bubble' label group in report; groups=%v", groupKeys(report.Groups))
	}
	values := bubbleValueSet(bubbleGroup.Bubbles)
	if _, ok := values["alpha"]; !ok {
		t.Fatalf("missing bubble=alpha in report: %v", values)
	}
	if _, ok := values["beta"]; !ok {
		t.Fatalf("missing bubble=beta in report: %v", values)
	}
	for _, b := range bubbleGroup.Bubbles {
		if b.Value != "alpha" && b.Value != "beta" {
			continue
		}
		if len(b.GoroutineIDs) < 1 {
			t.Fatalf("bubble %s has no goroutines", b.Value)
		}
	}
}

func groupKeys(gs []bubblereport.LabelGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Key)
	}
	return out
}

func bubbleValueSet(bs []bubblereport.Bubble) map[string]struct{} {
	out := make(map[string]struct{}, len(bs))
	for _, b := range bs {
		out[b.Value] = struct{}{}
	}
	return out
}
