package cli

import (
	"bytes"
	"testing"
)

func TestDiffUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"before", "after"}, {"before", "after", "--by", "tenant", "--labels", "x=y"}, {"before", "after", "--a", "x=y"}, {"before", "after", "extra", "--by", "tenant"}, {"before", "after", "--by", "tenant", "--format", "csv"}} {
		var out, errs bytes.Buffer
		if code := Main(append([]string{"diff"}, args...), &out, &errs); code != exitUsage {
			t.Fatalf("%v: %d %s", args, code, &errs)
		}
	}
}
