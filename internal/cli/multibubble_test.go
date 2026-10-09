package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

func TestMultiBubbleUsage(t *testing.T) {
	cases := [][]string{
		{"top", "missing.tar"},
		{"top", "missing.tar", "--by", "tenant", "--sort", "bad"},
		{"top", "missing.tar", "--by", "tenant", "--limit", "-1"},
		{"compare", "missing.tar", "--a", "tenant=a"},
		{"shared", "missing.tar", "--a", "tenant=a", "--b", "tenant=b", "--limit", "-1"},
		{"exclusive", "missing.tar"},
		{"exclusive", "missing.tar", "--labels", "tenant=a", "--format", "yaml"},
	}
	for _, args := range cases {
		var out, err bytes.Buffer
		if code := Main(args, &out, &err); code != exitUsage {
			t.Fatalf("%v: %d %s %s", args, code, &out, &err)
		}
	}
	for _, command := range []string{"top", "compare", "shared", "exclusive"} {
		args := []string{command, "/nonexistent.tar"}
		switch command {
		case "top":
			args = append(args, "--by", "tenant")
		case "exclusive":
			args = append(args, "--labels", "tenant=a")
		default:
			args = append(args, "--a-label", "tenant=a,comma", "--b-label", "tenant=b,comma")
		}
		var out, err bytes.Buffer
		if code := Main(args, &out, &err); code != exitFailure || !strings.Contains(err.String(), "/nonexistent.tar") {
			t.Fatalf("%v: %d %s %s", args, code, &out, &err)
		}
	}
}

func TestMultiBubbleText(t *testing.T) {
	var out bytes.Buffer
	writeMultiBubbleText(&out, &memusage.TopResult{By: "tenant", Groups: []memusage.GroupResult{{Value: "a\nrow", Breakdown: memusage.Breakdown{Reachable: memusage.Measure{Objects: 2, Bytes: 20}}}}})
	for _, part := range []string{`"a\nrow"`, "REACHABLE BYTES", "Bubbles only", "Other roots only", "Both", "Global overlap", "System overlap", "Ungrouped overlap", "not additive"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("missing %q: %s", part, &out)
		}
	}
	out.Reset()
	writeMultiBubbleText(&out, &memusage.SharedResult{Comparison: memusage.Comparison{OverlappingGoroutines: []uint64{7}}, Objects: []memusage.SharedObject{{Address: "0x100000000", Size: 10}}, Truncated: true})
	for _, part := range []string{"overlapping goroutines: [7]", "0x100000000", "truncated: true", "not process-wide"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("missing %q: %s", part, &out)
		}
	}
}
