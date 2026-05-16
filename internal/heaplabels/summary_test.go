package heaplabels

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestFormatLabelsEmpty(t *testing.T) {
	if got := FormatLabels(nil); got != nil {
		t.Fatalf("FormatLabels(nil) = %v", got)
	}
	if got := FormatLabels(map[string]string{}); got != nil {
		t.Fatalf("FormatLabels({}) = %v", got)
	}
}

func TestFormatLabelsSortedKeys(t *testing.T) {
	in := map[string]string{
		"job":    "42",
		"bubble": "alpha",
		"tenant": "acme",
	}
	got := FormatLabels(in)
	want := []string{"bubble=alpha", "job=42", "tenant=acme"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPrintSummary(t *testing.T) {
	r := Result{
		Stats: Stats{
			GoroutinesTotal:       7,
			GoroutinesDecoded:     5,
			GoroutinesNoLabels:    1,
			GoroutinesUnsupported: 0,
			GoroutinesFailed:      1,
			LabelsTotal:           9,
			StringsMissing:        2,
		},
	}
	var buf bytes.Buffer
	r.PrintSummary(&buf)
	got := buf.String()
	for _, want := range []string{
		"heap label recovery:",
		"goroutines: 7",
		"decoded labels: 5",
		"no labels: 1",
		"unsupported: 0",
		"failed: 1",
		"total label pairs: 9",
		"string bytes missing: 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintSummaryNoMissingStrings(t *testing.T) {
	r := Result{Stats: Stats{GoroutinesTotal: 1, StringsMissing: 0}}
	var buf bytes.Buffer
	r.PrintSummary(&buf)
	if strings.Contains(buf.String(), "string bytes missing") {
		t.Fatalf("should not print string-missing line when 0:\n%s", buf.String())
	}
}
