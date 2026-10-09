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
