package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"bundle.tar"}, {"bundle.tar", "--labels", "x=y", "--format", "yaml"}, {"bundle.tar", "extra", "--labels", "x=y"}} {
		var out, errs bytes.Buffer
		if code := Main(append([]string{"export"}, args...), &out, &errs); code != exitUsage {
			t.Fatalf("%v: %d %s", args, code, &errs)
		}
	}
}

func TestExportPprofOptions(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--pprof-mode", "bad"}, "pprof mode must"},
		{[]string{"--pprof-mode", "paths", "--max-path-depth", "-1"}, "max path depth must"},
		{[]string{"--pprof-mode", "paths", "--max-path-depth", "1"}, "max path depth must"},
		{[]string{"--max-path-depth", "2"}, "requires pprof paths"},
		{[]string{"--max-path-depth", "0"}, "requires pprof paths"},
		{[]string{"--format", "json", "--pprof-mode", "paths"}, "requires pprof format"},
		{[]string{"--format", "json", "--pprof-mode", "summary"}, "requires pprof format"},
		{[]string{"--format", "json", "--max-path-depth", "2"}, "requires pprof paths"},
	} {
		t.Run(strings.Join(tc.flags, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			output := filepath.Join(t.TempDir(), "keep.pb.gz")
			if err := os.WriteFile(output, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"export", "missing.tar", "--labels", "role=worker", "-o", output}, tc.flags...)
			if code := Main(args, &out, &stderr); code != exitUsage || !strings.Contains(stderr.String(), tc.want) || out.Len() != 0 {
				t.Fatal(code, stderr.String(), out.String())
			}
			data, err := os.ReadFile(output)
			if err != nil || string(data) != "keep" {
				t.Fatal("invalid flags changed destination", string(data), err)
			}
		})
	}
}

func TestExportAnalysisFailurePreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.pb.gz")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Main([]string{"export", "missing.tar", "--labels", "role=worker", "--pprof-mode", "paths", "-o", path}, &out, &stderr)
	data, err := os.ReadFile(path)
	if code != exitFailure || out.Len() != 0 || err != nil || string(data) != "keep" {
		t.Fatal(code, out.String(), stderr.String(), string(data), err)
	}
}
