package bubbleprof

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

	"delve_first_project/internal/snapshot"
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
}
