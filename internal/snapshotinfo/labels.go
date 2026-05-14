package snapshotinfo

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"bubblepprof/internal/bubblereport"
	"bubblepprof/internal/goroutineprofile"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/labelresolve"
	"bubblepprof/internal/snapshot"
	"bubblepprof/internal/snapshotgraph"
	"bubblepprof/internal/snapshotparse"
)

// PrintLabels parses a snapshot tar, resolves goroutine labels, and writes
// a diagnostic summary of the resolution.
func PrintLabels(out io.Writer, path string, opts labelresolve.Options) error {
	res, err := loadForBubbles(path, shouldKeepHeapContents(opts))
	if err != nil {
		return err
	}

	prof, profErr := parseProfile(res.GoroutineProfile)
	if profErr != nil && opts.Source == labelresolve.SourceModeProfile {
		return fmt.Errorf("parse goroutine profile: %w", profErr)
	}

	resolution := labelresolve.ResolveLabels(res.Snapshot, res.Labels, prof, opts)
	if profErr != nil {
		resolution.Warnings = append(resolution.Warnings,
			fmt.Sprintf("goroutine.pprof parse error (ignored): %v", profErr))
	}

	fmt.Fprintf(out, "snapshot format: %s\n", res.Metadata.Format)
	fmt.Fprintf(out, "heap goroutines: %d\n", resolution.HeapGoroutines)
	fmt.Fprintf(out, "profile samples: %d\n", resolution.ProfileSamples)
	fmt.Fprintf(out, "labels.json entries: %d\n", resolution.ManifestSize)
	printHeapMemoryMode(out, opts)
	printSourcePriority(out, opts)
	fmt.Fprintln(out)
	printLabelResolution(out, resolution)
	fmt.Fprintln(out)

	bySource := map[labelresolve.Source]int{}
	for _, s := range resolution.SourcesByGID {
		bySource[s]++
	}
	fmt.Fprintln(out, "label sources:")
	for _, s := range []labelresolve.Source{labelresolve.SourceHeap, labelresolve.SourceManifest, labelresolve.SourceProfileID, labelresolve.SourceProfileStack} {
		fmt.Fprintf(out, "  %s: %d\n", s, bySource[s])
	}
	fmt.Fprintf(out, "  unmatched heap goroutines: %d\n", resolution.UnmatchedHeap)
	fmt.Fprintf(out, "  unsupported heap layout: %t\n", resolution.Diagnostics.UnsupportedHeapLayout)
	fmt.Fprintln(out)

	keyVals := map[string]map[string]struct{}{}
	for _, labels := range resolution.LabelsByGID {
		for k, v := range labels {
			if labelresolve.IsCorrelationLabelKey(k) {
				continue
			}
			if keyVals[k] == nil {
				keyVals[k] = make(map[string]struct{})
			}
			keyVals[k][v] = struct{}{}
		}
	}
	if len(keyVals) == 0 {
		fmt.Fprintln(out, "label keys: (none)")
	} else {
		fmt.Fprintln(out, "label keys:")
		keys := make([]string, 0, len(keyVals))
		for k := range keyVals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vals := make([]string, 0, len(keyVals[k]))
			for v := range keyVals[k] {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			fmt.Fprintf(out, "  %s: %v\n", k, vals)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "attribution mode: %s\n", resolution.Diagnostics.Attribution.Description())

	if len(resolution.Warnings) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "warnings:")
		for _, w := range resolution.Warnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}
	return nil
}

// PrintBubbles parses a snapshot tar, resolves labels, builds the graph
// and the bubble report, and writes it.
func PrintBubbles(out io.Writer, path string, resOpts labelresolve.Options, repOpts bubblereport.Options) error {
	res, err := loadForBubbles(path, shouldKeepHeapContents(resOpts))
	if err != nil {
		return err
	}

	prof, profErr := parseProfile(res.GoroutineProfile)
	if profErr != nil && resOpts.Source == labelresolve.SourceModeProfile {
		return fmt.Errorf("parse goroutine profile: %w", profErr)
	}

	resolution := labelresolve.ResolveLabels(res.Snapshot, res.Labels, prof, resOpts)

	analysis, err := snapshotgraph.Build(res.Snapshot, snapshotgraph.Options{})
	if err != nil {
		return fmt.Errorf("build snapshot graph: %w", err)
	}
	// bubblereport needs whole-process reachability; Build is structural
	// only, so run the BFS pass here.
	snapshotgraph.ComputeReachability(analysis)

	report, err := bubblereport.Build(bubblereport.Input{
		Analysis:    analysis,
		LabelsByGID: resolution.LabelsByGID,
		Options:     repOpts,
	})
	if err != nil {
		return fmt.Errorf("build bubble report: %w", err)
	}

	fmt.Fprintf(out, "snapshot format: %s\n", res.Metadata.Format)
	fmt.Fprintf(out, "metadata go version: %s\n", res.Metadata.GoVersion)
	if res.Snapshot != nil {
		fmt.Fprintf(out, "heap dump build version: %s\n", res.Snapshot.Params.BuildVersion)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "label resolution:")
	printHeapMemoryMode(out, resOpts)
	printSourcePriority(out, resOpts)
	printLabelResolution(out, resolution)
	fmt.Fprintln(out)

	bySource := map[labelresolve.Source]int{}
	for _, s := range resolution.SourcesByGID {
		bySource[s]++
	}
	fmt.Fprintln(out, "label sources:")
	for _, s := range []labelresolve.Source{labelresolve.SourceHeap, labelresolve.SourceManifest, labelresolve.SourceProfileID, labelresolve.SourceProfileStack} {
		fmt.Fprintf(out, "  %s: %d\n", s, bySource[s])
	}
	fmt.Fprintf(out, "  unmatched heap goroutines: %d\n", resolution.UnmatchedHeap)
	fmt.Fprintf(out, "  unsupported heap layout: %t\n", resolution.Diagnostics.UnsupportedHeapLayout)
	if len(resolution.Warnings) > 0 {
		fmt.Fprintln(out, "label recovery warnings:")
		for _, w := range resolution.Warnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}
	fmt.Fprintln(out)

	report.PrintSummary(out)

	fmt.Fprintln(out, "bubble attribution source:")
	fmt.Fprintf(out, "  %s\n", resolution.Diagnostics.Attribution.Description())
	return nil
}

func printLabelResolution(out io.Writer, resolution labelresolve.Resolution) {
	fmt.Fprintln(out, "label resolution:")
	fmt.Fprintf(out, "  heap dump matches: %d\n", resolution.MatchedFromHeap)
	fmt.Fprintf(out, "  exact labels.json matches: %d\n", resolution.MatchedFromManifest)
	fmt.Fprintf(out, "  profile matches: %d\n", resolution.MatchedFromProfile)
	fmt.Fprintf(out, "  unmatched user goroutines: %d\n", resolution.UnmatchedHeap)
	fmt.Fprintf(out, "  unmatched profile samples: %d\n", resolution.UnmatchedProfile)
	fmt.Fprintf(out, "  ambiguous matches: %d\n", resolution.AmbiguousMatches)
}

func printSourcePriority(out io.Writer, opts labelresolve.Options) {
	switch opts.Source {
	case labelresolve.SourceModeHeap:
		fmt.Fprintln(out, "label source priority: heap-native only")
	case labelresolve.SourceModeManifest:
		fmt.Fprintln(out, "label source priority: labels.json only")
	case labelresolve.SourceModeProfile:
		fmt.Fprintln(out, "label source priority: goroutine.pprof only (best effort)")
	default:
		if opts.AllowProfileFallback {
			fmt.Fprintln(out, "label source priority: heap-native, labels.json fallback, goroutine.pprof best-effort fallback enabled")
		} else {
			fmt.Fprintln(out, "label source priority: heap-native, labels.json fallback, goroutine.pprof disabled (pass --allow-profile-fallback to opt in)")
		}
	}
}

func loadForBubbles(path string, keepHeapContents bool) (*snapshotparse.BubbleResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	res, err := snapshotparse.ParseSnapshotForBubbles(f, snapshotparse.BubbleParseOptions{
		HeapDump: heapdump.Options{KeepObjectContents: keepHeapContents},
	})
	if err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	return res, nil
}

// runLabels parses argv for the `snapshot labels` subcommand.
func runLabels(out, errOut io.Writer, program string, args []string) int {
	fs := flag.NewFlagSet("snapshot labels", flag.ContinueOnError)
	fs.SetOutput(errOut)
	labelsSource := fs.String("labels-source", "auto", "label source: auto|heap|manifest|profile; auto and heap retain heap object contents and may use more memory")
	allowProfile := fs.Bool("allow-profile-fallback", false, "in auto mode, allow goroutine.pprof best-effort fallback for goroutines not covered by heap-native or labels.json")
	requireHeap := fs.Bool("require-heap-labels", false, "warn loudly when heap-native recovery is unavailable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usage(errOut, program)
		return 2
	}
	opts, err := labelOptionsFromFlag(*labelsSource, *allowProfile, *requireHeap)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if err := PrintLabels(out, rest[0], opts); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// runBubbles parses argv for the `snapshot bubbles` subcommand.
func runBubbles(out, errOut io.Writer, program string, args []string) int {
	fs := flag.NewFlagSet("snapshot bubbles", flag.ContinueOnError)
	fs.SetOutput(errOut)
	labelKey := fs.String("label-key", "", "limit report to this label key")
	includeSystem := fs.Bool("include-system", false, "include system/background goroutines in user bubbles")
	includeUnlabeled := fs.Bool("include-unlabeled", false, "include unlabeled user goroutines as an <unlabeled> bubble")
	labelsSource := fs.String("labels-source", "auto", "label source: auto|heap|manifest|profile; auto and heap retain heap object contents and may use more memory")
	allowProfile := fs.Bool("allow-profile-fallback", false, "in auto mode, allow goroutine.pprof best-effort fallback for goroutines not covered by heap-native or labels.json")
	requireHeap := fs.Bool("require-heap-labels", false, "warn loudly when heap-native recovery is unavailable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usage(errOut, program)
		return 2
	}
	resOpts, err := labelOptionsFromFlag(*labelsSource, *allowProfile, *requireHeap)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	repOpts := bubblereport.Options{
		IncludeSystem:    *includeSystem,
		IncludeUnlabeled: *includeUnlabeled,
		LabelKeyFilter:   *labelKey,
	}
	if err := PrintBubbles(out, rest[0], resOpts, repOpts); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func labelOptionsFromFlag(src string, allowProfileFallback, requireHeap bool) (labelresolve.Options, error) {
	opts := labelresolve.Options{
		AllowProfileFallback: allowProfileFallback,
		RequireHeapLabels:    requireHeap,
	}
	switch src {
	case "", "auto":
		opts.Source = labelresolve.SourceModeAuto
	case "heap":
		opts.Source = labelresolve.SourceModeHeap
	case "manifest":
		opts.Source = labelresolve.SourceModeManifest
	case "profile":
		opts.Source = labelresolve.SourceModeProfile
	default:
		return labelresolve.Options{}, fmt.Errorf("unknown --labels-source %q", src)
	}
	return opts, nil
}

// shouldKeepHeapContents reports whether heap-native recovery can use
// retained object contents in this resolution. Only auto and heap modes
// need the contents.
func shouldKeepHeapContents(opts labelresolve.Options) bool {
	if opts.DisableHeap {
		return false
	}
	switch opts.Source {
	case labelresolve.SourceModeAuto, labelresolve.SourceModeHeap:
		return true
	}
	return false
}

func printHeapMemoryMode(out io.Writer, opts labelresolve.Options) {
	if shouldKeepHeapContents(opts) {
		fmt.Fprintln(out, "heap-native recovery: enabled (heap object contents retained; may use more memory)")
		return
	}
	fmt.Fprintln(out, "heap-native recovery: disabled (heap object contents not retained)")
}

// parseProfile is a tolerant wrapper around goroutineprofile.Parse: it
// returns (nil, nil) when the profile is empty, (profile, nil) on
// success, and (nil, err) on a real parse failure. Callers may choose
// to ignore profErr when labels.json is present.
func parseProfile(b []byte) (*goroutineprofile.Profile, error) {
	if len(b) == 0 {
		return nil, nil
	}
	return goroutineprofile.Parse(b)
}

// reference to snapshot.LabelsFile so the import never appears unused
// when the file is later trimmed.
var _ = snapshot.LabelsFile
