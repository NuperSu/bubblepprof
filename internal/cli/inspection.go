package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runInspection(command string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	labels, a, b := labelsFlag{}, labelsFlag{}, labelsFlag{}
	fs.Var(labels, "labels", "AND selector key=value[,key=value...] (repeatable)")
	fs.Var(exactLabelFlag{labels}, "label", "one exact key=value pair (repeatable)")
	by := fs.String("by", "", "report reachability from groups of this label key")
	groupLimit := fs.Int("group-limit", 20, "maximum membership values per object (0 uses 20)")
	include := fs.Bool("include-system", false, "let system/background goroutines participate in selectors and groups")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	gc := fs.Bool("gc", true, "run GC before fetching a capture")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout")
	format := fs.String("format", "text", "output format: text or json")
	var limit, top, pathObjects, maxPaths, maxSteps, maxVisited int
	var sortBy, object string
	switch command {
	case "objects":
		fs.IntVar(&limit, "limit", 20, "maximum displayed objects (0 returns all)")
		fs.StringVar(&sortBy, "sort", "size", "sort by size or address")
	case "explain":
		fs.Var(a, "a", "additional selector A key=value[,key=value...] (repeatable)")
		fs.Var(b, "b", "additional selector B key=value[,key=value...] (repeatable)")
		fs.Var(exactLabelFlag{a}, "a-label", "one exact A key=value pair")
		fs.Var(exactLabelFlag{b}, "b-label", "one exact B key=value pair")
		fs.StringVar(&object, "object", "", "allocation address or interior pointer (hex or decimal)")
		fs.IntVar(&top, "top", 20, "aggregate mode: largest objects per reachable/shared/exclusive list")
		fs.IntVar(&pathObjects, "path-objects", 3, "aggregate mode: number of representative objects to explain")
		fs.IntVar(&maxPaths, "max-paths", 6, "maximum witness paths per object (0 uses 6)")
		fs.IntVar(&maxSteps, "max-steps", 64, "maximum displayed objects per witness (0 uses 64)")
		fs.IntVar(&maxVisited, "max-visited", 0, "maximum visited objects per path search (0 permits V); exhaustion is explicit")
	}
	target, err := parseWithOneArg(fs, args)
	if err == flag.ErrHelp {
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		fs.Usage()
		return exitUsage
	}
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max, KeepReferenceProvenance: true}
	selectors := []memusage.Request{}
	for _, selector := range []labelsFlag{labels, a, b} {
		if len(selector) > 0 {
			selectors = append(selectors, memusage.Request{Labels: selector})
		}
	}
	switch {
	case *format != "text" && *format != "json":
		err = fmt.Errorf("format must be text or json")
	case len(selectors) == 0:
		err = fmt.Errorf("at least one label selector is required")
	case *groupLimit < 0 || limit < 0 || pathObjects < 0 || maxPaths < 0 || maxSteps < 0 || maxVisited < 0:
		err = fmt.Errorf("limits must be non-negative")
	case command == "objects" && sortBy != "size" && sortBy != "address":
		err = fmt.Errorf("sort must be size or address")
	case command == "explain" && object == "" && top <= 0:
		err = fmt.Errorf("top must be positive")
	case command == "explain" && object == "" && len(selectors) != 1:
		err = fmt.Errorf("aggregate explanations require one selector; use --object for multiple selectors")
	}
	var address uint64
	if err == nil && object != "" {
		address, err = strconv.ParseUint(object, 0, 64)
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "top" || f.Name == "path-objects" {
				err = fmt.Errorf("--object cannot be combined with --%s", f.Name)
			}
		})
	}
	if err == nil {
		for _, req := range selectors {
			if e := memusage.ValidateRequest(&req, opts); e != nil {
				err = e
				break
			}
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
	explainOpts := memusage.ExplainOptions{By: *by, MaxGroups: *groupLimit, MaxPaths: maxPaths, MaxSteps: maxSteps, MaxVisited: maxVisited}
	switch {
	case command == "objects":
		result, err = s.Objects(context.Background(), selectors[0], memusage.ObjectOptions{By: *by, Sort: sortBy, Limit: limit, GroupLimit: *groupLimit})
	case object != "":
		result, err = s.Explain(context.Background(), address, selectors, explainOpts)
	default:
		result, err = s.ExplainTop(context.Background(), selectors[0], top, pathObjects, explainOpts)
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
		writeInspectionText(stdout, result)
		m := s.Metadata()
		fmt.Fprintf(stdout, "validation: %s; unresolved pointers: %d\n", m.Validation, m.Graph.UnresolvedPointers)
		for _, warning := range m.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
	}
	return exitOK
}

func writeInspectionText(w io.Writer, result any) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	allocation := func(o memusage.ObjectInfo) {
		for _, sample := range o.AllocationSamples {
			fmt.Fprintf(tw, "Sampled allocation evidence for %s: sample=%s offset=%d bucket=%s allocation_size=%d bucket_allocs=%d bucket_frees=%d\n", o.Address, sample.SampleAddress, sample.Offset, sample.BucketAddress, sample.Profile.Size, sample.Profile.Allocs, sample.Profile.Frees)
			for _, f := range sample.Profile.Frames {
				fmt.Fprintf(tw, "  allocation frame: %s %s:%d\n", f.Function, f.File, f.Line)
			}
		}
	}
	list := func(objects []memusage.ObjectInfo, truncated bool) {
		fmt.Fprintln(tw, "ID\tADDRESS\tSHALLOW BYTES\tOUTGOING\tCLASSIFICATION\tGLOBAL\tOTHER SYSTEM\tOTHER GOROUTINES\tGROUPS (COUNT)")
		for _, o := range objects {
			fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\t%t\t%t\t%t\t%q (%d; truncated=%t)\n", o.ID, o.Address, o.Size, o.OutgoingReferences, o.Classification, o.Global, o.OtherSystem, o.OtherGoroutines, o.Groups, o.GroupCount, o.GroupsTruncated)
			allocation(o)
		}
		fmt.Fprintf(tw, "objects shown: %d; truncated: %t\n", len(objects), truncated)
	}
	summary := func(r memusage.ObjectsResult) {
		fmt.Fprintf(tw, "labels: %s; matched goroutines: %d; grouping label: %q\n", selectorText(r.Labels), r.Goroutines, r.By)
		fmt.Fprintln(tw, "SET\tOBJECTS\tBYTES")
		for _, part := range []struct {
			name string
			m    memusage.Measure
		}{{"Reachable", r.Reachable}, {"Exclusive", r.Exclusive}, {"Shared", r.Shared}} {
			fmt.Fprintf(tw, "%s\t%d\t%d\n", part.name, part.m.Objects, part.m.Bytes)
		}
		fmt.Fprintln(tw, "SIZE RANGE (BYTES)\tOBJECTS\tBYTES")
		for _, bucket := range r.Distribution {
			fmt.Fprintf(tw, "%d..%d\t%d\t%d\n", bucket.Min, bucket.Max, bucket.Objects, bucket.Bytes)
		}
		fmt.Fprintln(tw, "Largest reachable objects:")
		list(r.Objects, r.Truncated)
	}
	explanation := func(r memusage.ObjectExplanation) {
		fmt.Fprintf(tw, "\nObject %s (ID %d); shallow bytes: %d; classification: %s\n", r.Object.Address, r.Object.ID, r.Object.Size, r.Object.Classification)
		allocation(r.Object)
		fmt.Fprintf(tw, "root relationships: global=%t other_system=%t other_goroutines=%t\n", r.Object.Global, r.Object.OtherSystem, r.Object.OtherGoroutines)
		fmt.Fprintf(tw, "groups by %q: %q; total: %d; truncated: %t\n", r.By, r.Object.Groups, r.Object.GroupCount, r.Object.GroupsTruncated)
		for _, selection := range r.Selections {
			fmt.Fprintf(tw, "selection %s: matched=%d reachable=%t\n", selectorText(selection.Labels), selection.Goroutines, selection.Reachable)
		}
		fmt.Fprintf(tw, "reachable sources: %d; paths shown: %d; paths truncated: %t\n", r.ReachableSources, len(r.Paths), r.PathsTruncated)
		for _, p := range r.Paths {
			fmt.Fprintf(tw, "Path from %s %s: %s\n", p.Category, selectorText(p.Labels), p.Status)
			if p.Root != nil {
				root := p.Root
				fmt.Fprintf(tw, "  root: kind=%q goroutine=%d pointer=%s slot=%s detail=%q\n", root.Kind, root.GoroutineID, root.Pointer, root.Slot, root.Detail)
				if root.Frame != nil {
					f := root.Frame
					fmt.Fprintf(tw, "  frame: sp=%s depth=%d entry_pc=%s current_pc=%s continue_pc=%s\n", f.SP, f.Depth, f.EntryPC, f.CurrentPC, f.ContinuePC)
				}
			}
			for _, step := range p.Steps {
				fmt.Fprintf(tw, "  -> %s (ID %d; size %d)", step.Address, step.ID, step.Size)
				if step.IncomingOffset != nil {
					fmt.Fprintf(tw, " via previous allocation offset %d", *step.IncomingOffset)
				}
				if step.IncomingPointer != "" {
					fmt.Fprintf(tw, " pointer=%s", step.IncomingPointer)
				}
				fmt.Fprintln(tw)
			}
			fmt.Fprintf(tw, "  steps shown: %d of %d; truncated: %t\n", len(p.Steps), p.TotalSteps, p.Truncated)
		}
	}
	switch r := result.(type) {
	case *memusage.ObjectsResult:
		summary(*r)
	case *memusage.ObjectExplanation:
		explanation(*r)
	case *memusage.AggregateExplanation:
		summary(r.ObjectsResult)
		fmt.Fprintln(tw, "Largest shared objects:")
		list(r.LargestShared, r.SharedTruncated)
		fmt.Fprintln(tw, "Largest exclusively reachable objects:")
		list(r.LargestExclusive, r.ExclusiveTruncated)
		fmt.Fprintf(tw, "Representative explanations: %d; truncated: %t\n", len(r.Explanations), r.RepresentativesTruncated)
		for _, e := range r.Explanations {
			explanation(e)
		}
	}
	fmt.Fprintln(tw, "Classification is relative to the first selector and all other modeled roots.")
	fmt.Fprintln(tw, "A witness proves reachability; it need not be the only path, and removing a reference need not free the object.")
	tw.Flush()
}
