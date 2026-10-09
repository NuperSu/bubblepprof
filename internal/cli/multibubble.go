package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runMultiBubble(command string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	include := fs.Bool("include-system", false, "let system/background goroutines participate in selections")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	gc := fs.Bool("gc", true, "run GC before fetching a capture")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout")
	format := fs.String("format", "text", "output format: text or json")
	a, b, labels := labelsFlag{}, labelsFlag{}, labelsFlag{}
	var by, sortBy string
	var limit int
	switch command {
	case "top":
		fs.StringVar(&by, "by", "", "group by this existing pprof label key")
		fs.StringVar(&sortBy, "sort", "reachable", "sort by reachable, exclusive, or shared bytes")
		fs.IntVar(&limit, "limit", 0, "maximum displayed groups (0 means all)")
	case "compare", "shared":
		fs.Var(a, "a", "A selector key=value[,key=value...] (repeatable)")
		fs.Var(b, "b", "B selector key=value[,key=value...] (repeatable)")
		fs.Var(exactLabelFlag{a}, "a-label", "one exact A key=value pair (repeatable)")
		fs.Var(exactLabelFlag{b}, "b-label", "one exact B key=value pair (repeatable)")
		if command == "shared" {
			fs.IntVar(&limit, "limit", 20, "maximum displayed shared objects (0 means all)")
		}
	case "exclusive":
		fs.Var(labels, "labels", "selector key=value[,key=value...] (repeatable)")
		fs.Var(exactLabelFlag{labels}, "label", "one exact key=value pair (repeatable)")
	}
	target, err := parseWithOneArg(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fs.Usage()
		return exitUsage
	}
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max}
	switch {
	case *format != "text" && *format != "json":
		err = fmt.Errorf("format must be text or json")
	case limit < 0:
		err = fmt.Errorf("limit must be non-negative")
	case command == "top":
		if by == "" {
			err = fmt.Errorf("-by is required")
		} else if sortBy != "reachable" && sortBy != "exclusive" && sortBy != "shared" {
			err = fmt.Errorf("sort must be reachable, exclusive, or shared")
		}
	case command == "exclusive":
		if e := memusage.ValidateRequest(&memusage.Request{Labels: labels}, opts); e != nil {
			err = e
		}
	default:
		if e := memusage.ValidateRequest(&memusage.Request{Labels: a}, opts); e != nil {
			err = e
		} else if e := memusage.ValidateRequest(&memusage.Request{Labels: b}, opts); e != nil {
			err = e
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	bundle, err := loadBundle(target, *gc, *timeout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer bundle.Close()
	s, err := offline.LoadBundle(context.Background(), bundle, opts)
	if err != nil {
		_, body := memusage.ErrorResponseFor(err)
		writeMemUsageOutput(stdout, *format, body)
		return exitFailure
	}
	defer s.Close()
	var result any
	switch command {
	case "top":
		result, err = s.Top(context.Background(), memusage.TopOptions{By: by, Sort: sortBy, Limit: limit})
	case "compare":
		result, err = s.Compare(context.Background(), memusage.Request{Labels: a}, memusage.Request{Labels: b})
	case "shared":
		result, err = s.Shared(context.Background(), memusage.Request{Labels: a}, memusage.Request{Labels: b}, limit)
	case "exclusive":
		result, err = s.Exclusive(context.Background(), memusage.Request{Labels: labels})
	}
	if err != nil {
		_, body := memusage.ErrorResponseFor(err)
		writeMemUsageOutput(stdout, *format, body)
		return exitFailure
	}
	if *format == "json" {
		writeIndentedJSON(stdout, struct {
			Capture  any                       `json:"capture"`
			Metadata memusage.SnapshotMetadata `json:"metadata"`
			Result   any                       `json:"result"`
		}{s.Capture, s.Metadata(), result})
	} else {
		writeMultiBubbleText(stdout, result)
		m := s.Metadata()
		fmt.Fprintf(stdout, "validation: %s; unresolved pointers: %d\n", m.Validation, m.Graph.UnresolvedPointers)
		for _, w := range m.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", w)
		}
	}
	return exitOK
}

func writeMultiBubbleText(w io.Writer, result any) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	measure := func(name string, m memusage.Measure) { fmt.Fprintf(tw, "%s\t%d\t%d\n", name, m.Objects, m.Bytes) }
	comparison := func(c memusage.Comparison) {
		fmt.Fprintf(tw, "A labels: %s; goroutines: %d\nB labels: %s; goroutines: %d\n", selectorText(c.A), c.AGoroutines, selectorText(c.B), c.BGoroutines)
		fmt.Fprintf(tw, "overlapping goroutines: %v\n", c.OverlappingGoroutines)
		fmt.Fprintln(tw, "SET\tOBJECTS\tBYTES")
		measure("A-only relative to B", c.AOnly)
		measure("B-only relative to A", c.BOnly)
		measure("Shared by A and B", c.Shared)
		measure("Union", c.Union)
		fmt.Fprintln(tw, "Pairwise differences are not process-wide exclusive reachability.")
	}
	switch r := result.(type) {
	case *memusage.TopResult:
		fmt.Fprintf(tw, "grouping label: %q; groups: %d; missing label: %d goroutines; excluded system: %d goroutines\n", r.By, r.TotalGroups, r.UngroupedGoroutines, r.ExcludedSystemGoroutines)
		fmt.Fprintln(tw, "VALUE\tGOROUTINES\tREACHABLE BYTES\tEXCLUSIVE BYTES\tSHARED BYTES")
		for _, g := range r.Groups {
			fmt.Fprintf(tw, "%q\t%d\t%d\t%d\t%d\n", g.Value, g.Goroutines, g.Reachable.Bytes, g.Exclusive.Bytes, g.Shared.Bytes)
		}
		fmt.Fprintln(tw, "\nDetailed counts (distinct objects and shallow bytes):")
		for _, g := range r.Groups {
			fmt.Fprintf(tw, "group %q\nCATEGORY\tOBJECTS\tBYTES\n", g.Value)
			measure("Reachable", g.Reachable)
			measure("Exclusive", g.Exclusive)
			measure("Bubbles only", g.BubblesOnly)
			measure("Other roots only", g.OtherRootsOnly)
			measure("Both", g.Both)
			measure("Bubble overlap", g.BubbleOverlap)
			measure("Other root overlap", g.OtherRootOverlap)
			measure("Global overlap", g.GlobalOverlap)
			measure("System overlap", g.SystemOverlap)
			measure("Ungrouped overlap", g.UngroupedOverlap)
		}
		fmt.Fprintln(tw, "Row totals are not additive. Overlap categories can overlap each other.")
	case *memusage.Comparison:
		comparison(*r)
	case *memusage.SharedResult:
		comparison(r.Comparison)
		fmt.Fprintln(tw, "ID\tADDRESS\tSHALLOW BYTES\tOUTGOING REFERENCES")
		for _, o := range r.Objects {
			fmt.Fprintf(tw, "%d\t%s\t%d\t%d\n", o.ID, o.Address, o.Size, o.OutgoingReferences)
		}
		fmt.Fprintf(tw, "objects shown: %d; truncated: %t\n", len(r.Objects), r.Truncated)
	case *memusage.ExclusiveResult:
		fmt.Fprintf(tw, "labels: %s; goroutines: %d\n", selectorText(r.Labels), r.Goroutines)
		fmt.Fprintln(tw, "SET\tOBJECTS\tBYTES")
		measure("Reachable", r.Reachable)
		measure("Exclusive", r.Exclusive)
		measure("Shared with other roots", r.Shared)
		measure("Global overlap", r.GlobalOverlap)
		measure("Non-selected system overlap", r.SystemOverlap)
		measure("Other goroutine overlap", r.OtherGoroutineOverlap)
		fmt.Fprintln(tw, "Exclusive is relative to all other modeled roots; overlap categories are not additive.")
	}
	tw.Flush()
}

func selectorText(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%q=%q", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
