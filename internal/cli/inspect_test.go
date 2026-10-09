package cli

import (
	"bytes"
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
