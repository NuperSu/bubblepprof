package snapshotinfo

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/labelresolve"
	"bubblepprof/internal/snapshot"
)

// buildLabeledSnapshot creates a snapshot tar that exercises both the
// heap-native-eligible heap and an exact labels.json manifest. Returns
// the path to a tempfile.
func buildLabeledSnapshot(t *testing.T) string {
	t.Helper()
	heap := buildHeapDumpWithLabels()
	manifest := bubblelabels.Manifest{
		Format: bubblelabels.ManifestFormatV1,
		Goroutines: []bubblelabels.GoroutineLabels{
			{ID: 123, Labels: map[string]string{"bubble": "manifest"}},
		},
	}
	mbytes, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte{},
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels: mbytes,
	})
}

func TestLabelOptionsFromFlag(t *testing.T) {
	cases := []struct {
		flag   string
		want   labelresolve.SourceMode
		errSub string
	}{
		{"", labelresolve.SourceModeAuto, ""},
		{"auto", labelresolve.SourceModeAuto, ""},
		{"heap", labelresolve.SourceModeHeap, ""},
		{"manifest", labelresolve.SourceModeManifest, ""},
		{"profile", labelresolve.SourceModeProfile, ""},
		{"bogus", "", "unknown --labels-source"},
	}
	for _, tc := range cases {
		opts, err := labelOptionsFromFlag(tc.flag, false, false)
		if tc.errSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("flag %q: err = %v, want substring %q", tc.flag, err, tc.errSub)
			}
			continue
		}
		if err != nil {
			t.Errorf("flag %q: unexpected err %v", tc.flag, err)
			continue
		}
		if opts.Source != tc.want {
			t.Errorf("flag %q: Source = %q, want %q", tc.flag, opts.Source, tc.want)
		}
	}

	// Verify AllowProfileFallback + RequireHeapLabels flow through.
	opts, err := labelOptionsFromFlag("auto", true, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !opts.AllowProfileFallback || !opts.RequireHeapLabels {
		t.Fatalf("flags did not propagate: %+v", opts)
	}
}

func TestPrintSourcePriorityAllModes(t *testing.T) {
	cases := []struct {
		mode  labelresolve.SourceMode
		allow bool
		want  string
	}{
		{labelresolve.SourceModeHeap, false, "heap-native only"},
		{labelresolve.SourceModeManifest, false, "labels.json only"},
		{labelresolve.SourceModeProfile, false, "goroutine.pprof only (best effort)"},
		{labelresolve.SourceModeAuto, false, "goroutine.pprof disabled"},
		{labelresolve.SourceModeAuto, true, "goroutine.pprof best-effort fallback enabled"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		printSourcePriority(&buf, labelresolve.Options{Source: tc.mode, AllowProfileFallback: tc.allow})
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("mode=%q allow=%t: missing %q in %q", tc.mode, tc.allow, tc.want, buf.String())
		}
	}
}

func TestShouldKeepHeapContents(t *testing.T) {
	cases := []struct {
		opts labelresolve.Options
		want bool
	}{
		{labelresolve.Options{}, true},
		{labelresolve.Options{Source: labelresolve.SourceModeHeap}, true},
		{labelresolve.Options{Source: labelresolve.SourceModeAuto}, true},
		{labelresolve.Options{Source: labelresolve.SourceModeManifest}, false},
		{labelresolve.Options{Source: labelresolve.SourceModeProfile}, false},
		{labelresolve.Options{DisableHeap: true}, false},
	}
	for i, tc := range cases {
		if got := shouldKeepHeapContents(tc.opts); got != tc.want {
			t.Errorf("case %d: shouldKeepHeapContents(%+v) = %t, want %t", i, tc.opts, got, tc.want)
		}
	}
}

func TestRunLabelsCLIBubblesEndToEnd(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"labels", "--labels-source", "manifest", path})
	if code != 0 {
		t.Fatalf("exit = %d, err=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{
		"label source priority: labels.json only",
		"heap-native recovery: disabled",
		"labels.json: 1",
		"attribution mode:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunBubblesCLIEndToEnd(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"bubbles", "--labels-source", "auto", "--allow-profile-fallback", path})
	if code != 0 {
		t.Fatalf("exit = %d, err=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{
		"label source priority: heap-native, labels.json fallback",
		"heap-native recovery: enabled",
		"bubble attribution source:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunBubblesCLIWithFilters(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{
		"bubbles",
		"--label-key", "bubble",
		"--include-system",
		"--include-unlabeled",
		"--labels-source", "manifest",
		path,
	})
	if code != 0 {
		t.Fatalf("exit = %d, err=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "label group: bubble") {
		t.Fatalf("missing label group in:\n%s", out.String())
	}
}

func TestRunBubblesRejectsBadSource(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"bubbles", "--labels-source", "nope", path})
	if code != 2 {
		t.Fatalf("expected rc=2, got %d (err=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "unknown --labels-source") {
		t.Fatalf("missing diagnostic: %q", errBuf.String())
	}
}

func TestRunBubblesRejectsBadArgs(t *testing.T) {
	cases := [][]string{
		{"bubbles"},
		{"labels"},
		{"heap-labels"},
		{"bubbles", "--labels-source", "auto"},
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		code := Run(&out, &errBuf, "bubblepprof", args)
		if code != 2 {
			t.Fatalf("args=%v: rc = %d, want 2 (err=%q)", args, code, errBuf.String())
		}
	}
}

func TestRunLabelsRejectsMissingFile(t *testing.T) {
	missing := t.TempDir() + "/does-not-exist.tar"
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"labels", missing})
	if code != 1 {
		t.Fatalf("rc = %d, want 1 (err=%q)", code, errBuf.String())
	}
}

func TestRunHeapLabelsBadOffset(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"heap-labels", "--g-labels-offset", "garbage", path})
	if code != 2 {
		t.Fatalf("rc = %d, want 2 (err=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "invalid --g-labels-offset") {
		t.Fatalf("missing diagnostic: %q", errBuf.String())
	}
}

func TestRunHeapLabelsBadFindOffset(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"heap-labels", "--find-offset", "noequals", path})
	if code != 2 {
		t.Fatalf("rc = %d, want 2 (err=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "invalid --find-offset") {
		t.Fatalf("missing diagnostic: %q", errBuf.String())
	}
}

func TestRunHeapLabelsEndToEnd(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"heap-labels", "--g-labels-offset", "0x18", path})
	if code != 0 {
		t.Fatalf("rc = %d, err=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{
		"runtime.g.labels offset: 0x18",
		"goroutine 123",
		"bubble=alpha",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunHeapLabelsShowFailed(t *testing.T) {
	// Goroutine has no g object contents → status g_object_missing.
	heap := buildHeapDumpWithLooseGoroutine()
	manifest := bubblelabels.Manifest{Format: bubblelabels.ManifestFormatV1}
	mbytes, _ := manifest.Marshal()
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte{},
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
		Labels: mbytes,
	})
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{
		"heap-labels",
		"--g-labels-offset", "0x18",
		"--show-failed",
		path,
	})
	if code != 0 {
		t.Fatalf("rc = %d, err=%q", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "non-decoded goroutines") {
		t.Fatalf("expected non-decoded section in:\n%s", got)
	}
}

func TestRunInfoCLI(t *testing.T) {
	path := buildLabeledSnapshot(t)
	var out, errBuf bytes.Buffer
	code := Run(&out, &errBuf, "bubblepprof", []string{"info", path})
	if code != 0 {
		t.Fatalf("rc = %d, err=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "labels.json: present") {
		t.Fatalf("expected labels.json present in:\n%s", out.String())
	}
}

// buildHeapDumpWithLooseGoroutine emits a goroutine whose g address does
// NOT correspond to a stored object — decoder should report
// g_object_missing.
func buildHeapDumpWithLooseGoroutine() []byte {
	var buf bytes.Buffer
	buf.WriteString("go1.7 heap dump\n")

	writeUvarint(&buf, 6) // tagParams
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 8)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeString(&buf, "amd64")
	writeString(&buf, "go-test")
	writeUvarint(&buf, 1)

	// One unrelated heap object (so KeepObjectContents has something to
	// retain).
	writeObject(&buf, 0x9000, []byte{0, 0, 0, 0, 0, 0, 0, 0})

	// Goroutine pointing to a g address with no object record.
	writeUvarint(&buf, 4)      // tagGoroutine
	writeUvarint(&buf, 0xdead) // g addr (unmatched)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 99)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeString(&buf, "")
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)

	writeUvarint(&buf, 0)
	return buf.Bytes()
}

// quiet os.Stdin in case any sub-call reads from it
var _ = os.Stdin
var _ = fmt.Sprintf
