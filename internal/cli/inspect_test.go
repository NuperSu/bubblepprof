package cli

import (
	"bytes"
	"encoding/json"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectUsageAndCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"inspect"}, {"inspect", "capture.tar", "--format", "bad"}, {"labels", "capture.tar", "--format", "bad"}} {
		var out, err bytes.Buffer
		if code := Main(args, &out, &err); code != exitUsage {
			t.Fatal(args, code)
		}
	}
	for _, command := range []string{"inspect", "query", "labels", "top", "diff", "export", "fetch"} {
		var out, err bytes.Buffer
		if code := Main([]string{command, "-h"}, &out, &err); code != exitOK {
			t.Fatal(command, code)
		}
	}
}

func TestHelpDoesNotReportMissingArguments(t *testing.T) {
	for _, command := range []string{"inspect", "query", "labels", "fetch", "top", "objects", "explain", "explore", "export", "diff"} {
		var out, err bytes.Buffer
		if code := Main([]string{command, "-h"}, &out, &err); code != exitOK {
			t.Fatal(command, code)
		}
		if bytes.Contains(err.Bytes(), []byte("missing argument")) || bytes.Contains(err.Bytes(), []byte("exactly one target URL is required")) || bytes.Contains(err.Bytes(), []byte("inspect requires")) {
			t.Fatal(command, err.String())
		}
	}
	var out, err bytes.Buffer
	if code := Main([]string{"unknown", "-h"}, &out, &err); code != exitUsage {
		t.Fatal("unknown command help succeeded")
	}
}

func TestInspectPreservesCaptureMetadataOnAnalysisFailure(t *testing.T) {
	var capture bytes.Buffer
	dump := []byte("unsupported heap data")
	if err := bundle.Write(&capture, bundle.WriteInput{Meta: bundle.Meta{Producer: "inspection-test"}, HeapDump: bytes.NewReader(dump), HeapDumpSize: int64(len(dump))}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "capture.tar")
	if err := os.WriteFile(path, capture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := Main([]string{"inspect", path, "--format", "json"}, &out, &errs); code != exitFailure {
		t.Fatal(code)
	}
	var report struct {
		Capture       bundle.Meta `json:"capture"`
		AnalysisError struct {
			Code string `json:"code"`
		} `json:"analysis_error"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Capture.Producer != "inspection-test" || report.AnalysisError.Code != "parse_failed" {
		t.Fatal(out.String())
	}
}
