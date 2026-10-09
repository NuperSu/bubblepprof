package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	by := fs.String("by", "", "grouping label key")
	format := fs.String("format", "text", "text or json")
	include := fs.Bool("include-system", false, "include system/background goroutines in selections")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	labels, a, b := labelsFlag{}, labelsFlag{}, labelsFlag{}
	fs.Var(labels, "labels", "selector key=value[,key=value...]")
	fs.Var(exactLabelFlag{labels}, "label", "one exact selector key=value")
	fs.Var(a, "a", "A selector")
	fs.Var(b, "b", "B selector")
	fs.Var(exactLabelFlag{a}, "a-label", "one exact A key=value")
	fs.Var(exactLabelFlag{b}, "b-label", "one exact B key=value")
	paths := []string{}
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return exitOK
			}
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		paths = append(paths, fs.Arg(0))
		args = fs.Args()[1:]
	}
	modes := 0
	if *by != "" {
		modes++
	}
	if len(labels) > 0 {
		modes++
	}
	if len(a) > 0 || len(b) > 0 {
		modes++
	}
	if len(paths) != 2 || modes != 1 || (len(a) > 0) != (len(b) > 0) || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "diff requires before.tar after.tar and exactly one of --by, --labels, or --a and --b; format must be text or json")
		return exitUsage
	}
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max}
	ctx := context.Background()
	before, err := offline.OpenSnapshot(ctx, paths[0], opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer before.Close()
	after, err := offline.OpenSnapshot(ctx, paths[1], opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer after.Close()
	r, err := offline.Diff(ctx, before, after, offline.DiffOptions{By: *by, Labels: labels, A: a, B: b})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	if *format == "json" {
		writeIndentedJSON(stdout, r)
		return exitOK
	}
	for _, c := range r.Compatibility {
		fmt.Fprintf(stdout, "compatibility %s: %s (before=%v after=%v)\n", c.Field, c.Status, c.Before, c.After)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SELECTOR\tPRESENT BEFORE/AFTER\tGOROUTINES BEFORE/AFTER\tROOTS BEFORE/AFTER\tMETRIC\tBEFORE BYTES\tAFTER BYTES\tDELTA BYTES\tDELTA OBJECTS")
	for _, g := range r.Groups {
		for _, k := range []string{"reachable", "exclusive", "shared", "global_overlap", "system_overlap", "other_goroutine_overlap"} {
			d := g.Metrics[k]
			fmt.Fprintf(tw, "%s\t%t/%t\t%d/%d\t%d/%d\t%s\t%d\t%d\t%s\t%d\n", selectorText(g.Labels), g.Before.Present, g.After.Present, g.Before.Goroutines, g.After.Goroutines, g.Before.Roots, g.After.Roots, k, g.Before.Metrics[k].Bytes, g.After.Metrics[k].Bytes, d.Bytes, d.Objects)
		}
		fmt.Fprintf(tw, "size distributions: before=%v after=%v delta=%v\nroot categories: before=%v after=%v delta=%v\nstack root functions: before=%v after=%v delta=%v\n", g.Before.Distribution, g.After.Distribution, g.Distribution, g.Before.RootCategories, g.After.RootCategories, g.RootCategories, g.Before.RootFunctions, g.After.RootFunctions, g.RootFunctions)
	}
	if r.Pair != nil {
		for _, k := range []string{"a_only", "b_only", "shared", "union"} {
			d := r.Pair.Delta[k]
			fmt.Fprintf(tw, "pair %s delta: %s bytes, %d objects\n", k, d.Bytes, d.Objects)
		}
	}
	tw.Flush()
	for _, n := range r.Notes {
		fmt.Fprintln(stdout, n)
	}
	return exitOK
}
