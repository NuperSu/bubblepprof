package cli

import (
	"bytes"
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
