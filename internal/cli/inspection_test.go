package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

func TestInspectionUsage(t *testing.T) {
	cases := [][]string{
		{"objects", "missing.tar"},
		{"objects", "missing.tar", "--labels", "tenant=a", "--sort", "bad"},
		{"objects", "missing.tar", "--labels", "tenant=a", "--limit", "-1"},
		{"explain", "missing.tar", "--labels", "tenant=a", "--object", "bad"},
		{"explain", "missing.tar", "--labels", "tenant=a", "--object", "0x1000", "--max-steps", "-1"},
		{"explain", "missing.tar", "--a", "tenant=a", "--b", "tenant=b"},
		{"explain", "missing.tar", "--labels", "tenant=a", "--top", "0"},
		{"explain", "missing.tar", "--labels", "tenant=a", "--object", "0x1000", "--top", "20"},
		{"objects", "missing.tar", "--labels", "tenant=a", "--format", "yaml"},
	}
	for _, args := range cases {
		var out, err bytes.Buffer
		if code := Main(args, &out, &err); code != exitUsage {
			t.Fatalf("%v: %d %s %s", args, code, &out, &err)
		}
	}
	for _, command := range []string{"objects", "explain"} {
		var out, err bytes.Buffer
		if code := Main([]string{command, "/nonexistent.tar", "--label", "tenant=a,comma"}, &out, &err); code != exitFailure || !strings.Contains(err.String(), "/nonexistent.tar") {
			t.Fatalf("%s: %d %s %s", command, code, &out, &err)
		}
	}
}

func TestInspectionText(t *testing.T) {
	var out bytes.Buffer
	offset := uint64(0)
	result := &memusage.AggregateExplanation{
		ObjectsResult: memusage.ObjectsResult{Labels: map[string]string{"tenant": "a"}, Distribution: []memusage.SizeBucket{{Min: 1, Max: 1, Measure: memusage.Measure{Objects: 200, Bytes: 200}}}, Objects: []memusage.ObjectInfo{{SharedObject: memusage.SharedObject{ID: 1, Address: "0x1000", Size: 1}, Classification: "shared"}}},
		Explanations:  []memusage.ObjectExplanation{{Object: memusage.ObjectInfo{SharedObject: memusage.SharedObject{Address: "0x1000"}, Groups: []string{"a", "b"}, GroupCount: 2}, PathsTruncated: true, Paths: []memusage.PathReport{{Category: "selection", Status: "found", Root: &memusage.RootWitness{GoroutineID: 42, Kind: "stack", Detail: "worker"}, Steps: []memusage.PathStep{{SharedObject: memusage.SharedObject{Address: "0x1000"}, IncomingOffset: &offset}}, TotalSteps: 4, Truncated: true}, {Category: "group", Status: "search_limit"}}}},
	}
	writeInspectionText(&out, result)
	for _, part := range []string{"Largest reachable", "Largest shared", "Largest exclusively", "SIZE RANGE", "200", "groups by", "goroutine=42", `detail="worker"`, "offset 0", "steps shown: 1 of 4; truncated: true", "search_limit", "paths truncated: true", "need not free"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("missing %q: %s", part, &out)
		}
	}
}
