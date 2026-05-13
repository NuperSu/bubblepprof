package snapshotinfo

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"bubblepprof/internal/snapshot"
)

func TestRunRejectsWrongArgumentCounts(t *testing.T) {
	tests := [][]string{
		nil,
		{"info"},
		{"parse"},
		{"graph"},
		{"graph", "snapshot.tar", "extra"},
	}
	for _, args := range tests {
		var out, errBuf bytes.Buffer
		code := Run(&out, &errBuf, "bubblepprof", args)
		if code != 2 {
			t.Fatalf("Run(%v) exit = %d, want 2", args, code)
		}
		if !strings.Contains(errBuf.String(), "usage:") {
			t.Fatalf("Run(%v) stderr missing usage: %q", args, errBuf.String())
		}
	}
}

func TestPrintGraphReportsBadHeapDump(t *testing.T) {
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, snapshot.BundleSource{
		HeapDump:         strings.NewReader("not a heap dump\n"),
		HeapDumpSize:     int64(len("not a heap dump\n")),
		GoroutineProfile: []byte("profile"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp snapshot: %v", err)
	}

	var out bytes.Buffer
	err := PrintGraph(&out, path)
	if err == nil || !strings.Contains(err.Error(), "parse snapshot") {
		t.Fatalf("err = %v, want parse snapshot error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("PrintGraph should not write partial summary on error:\n%s", out.String())
	}
}

func TestRunGraphSuccessExitCode(t *testing.T) {
	heap := buildMinimalHeapDump()
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("profile"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp snapshot: %v", err)
	}

	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"graph", path})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "snapshot format: bubblepprof-snapshot-v1") {
		t.Fatalf("graph output missing summary:\n%s", out.String())
	}
}
