package heaplabels

import (
	"fmt"
	"io"
	"sort"
)

func (r Result) PrintSummary(w io.Writer) {
	fmt.Fprintln(w, "heap label recovery:")
	fmt.Fprintf(w, "  goroutines: %d\n", r.Stats.GoroutinesTotal)
	fmt.Fprintf(w, "  decoded labels: %d\n", r.Stats.GoroutinesDecoded)
	fmt.Fprintf(w, "  no labels: %d\n", r.Stats.GoroutinesNoLabels)
	fmt.Fprintf(w, "  unsupported: %d\n", r.Stats.GoroutinesUnsupported)
	fmt.Fprintf(w, "  failed: %d\n", r.Stats.GoroutinesFailed)
	fmt.Fprintf(w, "  total label pairs: %d\n", r.Stats.LabelsTotal)
	if r.Stats.StringsMissing > 0 {
		fmt.Fprintf(w, "  string bytes missing: %d\n", r.Stats.StringsMissing)
	}
}

func FormatLabels(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, labels[k]))
	}
	return out
}
