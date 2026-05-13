package snapshotinfo

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/bubblereport"
	"bubblepprof/internal/labelresolve"
	"bubblepprof/internal/snapshot"
)

func writeBundleAt(t *testing.T, src snapshot.BundleSource) string {
	t.Helper()
	var tar bytes.Buffer
	if err := snapshot.WriteSnapshotBundle(&tar, src); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	path := t.TempDir() + "/snapshot.tar"
	if err := os.WriteFile(path, tar.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return path
}

func TestPrintLabelsWithManifest(t *testing.T) {
	heap := buildMinimalHeapDump()
	manifest := bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 7, Labels: map[string]string{"bubble": "alpha"}},
		},
	}
	mbytes, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels: mbytes,
	})

	var out bytes.Buffer
	if err := PrintLabels(&out, path, labelresolve.Options{}); err != nil {
		t.Fatalf("PrintLabels: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"heap goroutines: 1",
		"labels.json entries: 1",
		"exact labels.json matches: 1",
		"bubble: [alpha]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintBubblesIntegratesLabels(t *testing.T) {
	heap := buildMinimalHeapDump()
	manifest := bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 7, Labels: map[string]string{"bubble": "alpha"}},
		},
	}
	mbytes, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels: mbytes,
	})

	var out bytes.Buffer
	if err := PrintBubbles(&out, path, labelresolve.Options{}, bubblereport.Options{}); err != nil {
		t.Fatalf("PrintBubbles: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"label group: bubble",
		"bubble=alpha",
		"reachable bytes:",
		"bubble attribution source:",
		"labels.json exact",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintLabelsRejectsBadSource(t *testing.T) {
	heap := buildMinimalHeapDump()
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})

	var out, errBuf bytes.Buffer
	rc := Run(&out, &errBuf, "bubblepprof", []string{"labels", "--labels-source", "bogus", path})
	if rc != 2 {
		t.Fatalf("expected rc=2, got %d (errout=%s)", rc, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "unknown --labels-source") {
		t.Fatalf("missing diagnostic: %s", errBuf.String())
	}
}
