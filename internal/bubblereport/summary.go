package bubblereport

import (
	"fmt"
	"io"
)

// PrintSummary writes a stable, human-readable summary of the report.
func (r *Report) PrintSummary(w io.Writer) {
	if r == nil {
		fmt.Fprintln(w, "bubble report: <nil>")
		return
	}
	d := r.Diagnostics
	fmt.Fprintf(w, "heap goroutines: %d\n", d.HeapGoroutines)
	fmt.Fprintf(w, "user goroutines: %d (labeled: %d, unlabeled: %d)\n",
		d.UserGoroutines, d.LabeledUserGoroutines, d.UnlabeledUserGoroutines)
	fmt.Fprintf(w, "system goroutines: %d (ignored: %d)\n",
		d.SystemGoroutines, d.IgnoredSystemGoroutines)
	fmt.Fprintln(w)

	if len(r.Groups) == 0 {
		fmt.Fprintln(w, "no bubbles found")
		return
	}
	for _, group := range r.Groups {
		fmt.Fprintf(w, "label group: %s\n", group.Key)
		for _, b := range group.Bubbles {
			fmt.Fprintf(w, "  %s=%s\n", b.Key, b.Value)
			fmt.Fprintf(w, "    goroutines: %d\n", len(b.GoroutineIDs))
			fmt.Fprintf(w, "    reachable objects: %d\n", len(b.ReachableObjects))
			fmt.Fprintf(w, "    reachable bytes: %d\n", b.ReachableBytes)
			fmt.Fprintf(w, "    exclusive objects: %d\n", len(b.ExclusiveObjects))
			fmt.Fprintf(w, "    exclusive bytes: %d\n", b.ExclusiveBytes)
			fmt.Fprintf(w, "    shared objects: %d\n", len(b.SharedObjects))
			fmt.Fprintf(w, "    shared bytes: %d\n", b.SharedBytes)
			fmt.Fprintf(w, "    global overlap objects: %d\n", b.GlobalOverlapObjects)
			fmt.Fprintf(w, "    global overlap bytes: %d\n", b.GlobalOverlapBytes)
			fmt.Fprintf(w, "    system overlap objects: %d\n", b.SystemOverlapObjects)
			fmt.Fprintf(w, "    system overlap bytes: %d\n", b.SystemOverlapBytes)
		}
		fmt.Fprintf(w, "  group shared objects: %d\n", len(group.SharedObjects))
		fmt.Fprintf(w, "  group shared bytes: %d\n", group.SharedBytes)
		fmt.Fprintln(w)
	}
}
