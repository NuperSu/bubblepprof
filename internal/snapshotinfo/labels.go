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
	if profErr != nil && opts.ProfileOnly {
		return fmt.Errorf("parse goroutine profile: %w", profErr)
	}

	resolution := labelresolve.ResolveLabels(res.Snapshot, res.Labels, prof, opts)
	if profErr != nil {
		resolution.Warnings = append(resolution.Warnings,
			fmt.Sprintf("goroutine.pprof parse error (ignored because labels.json is present): %v", profErr))
	}

	fmt.Fprintf(out, "snapshot format: %s\n", res.Metadata.Format)
	fmt.Fprintf(out, "heap goroutines: %d\n", resolution.HeapGoroutines)
	fmt.Fprintf(out, "profile samples: %d\n", resolution.ProfileSamples)
	fmt.Fprintf(out, "labels.json entries: %d\n", resolution.ManifestSize)
	printHeapMemoryMode(out, opts)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "label resolution:")
	fmt.Fprintf(out, "  heap dump matches: %d\n", resolution.MatchedFromHeap)
	fmt.Fprintf(out, "  exact labels.json matches: %d\n", resolution.MatchedFromManifest)
	fmt.Fprintf(out, "  profile matches: %d\n", resolution.MatchedFromProfile)
	fmt.Fprintf(out, "  unmatched user goroutines: %d\n", resolution.UnmatchedHeap)
	fmt.Fprintf(out, "  unmatched profile samples: %d\n", resolution.UnmatchedProfile)
	fmt.Fprintf(out, "  ambiguous matches: %d\n", resolution.AmbiguousMatches)
	fmt.Fprintln(out)

	bySource := map[labelresolve.Source]int{}
	for _, s := range resolution.SourcesByGID {
		bySource[s]++
	}
	fmt.Fprintln(out, "label sources:")
	for _, s := range []labelresolve.Source{labelresolve.SourceHeap, labelresolve.SourceManifest, labelresolve.SourceProfileID, labelresolve.SourceProfileStack} {
		fmt.Fprintf(out, "  %s: %d\n", s, bySource[s])
	}
	fmt.Fprintln(out)

	keyVals := map[string]map[string]struct{}{}
	for _, labels := range resolution.LabelsByGID {
		for k, v := range labels {
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
	if profErr != nil && resOpts.ProfileOnly {
		return fmt.Errorf("parse goroutine profile: %w", profErr)
	}

	resolution := labelresolve.ResolveLabels(res.Snapshot, res.Labels, prof, resOpts)

	analysis, err := snapshotgraph.Build(res.Snapshot, snapshotgraph.Options{})
	if err != nil {
		return fmt.Errorf("build snapshot graph: %w", err)
	}

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
	fmt.Fprintf(out, "  heap dump matches: %d\n", resolution.MatchedFromHeap)
	fmt.Fprintf(out, "  exact labels.json matches: %d\n", resolution.MatchedFromManifest)
	fmt.Fprintf(out, "  profile matches: %d\n", resolution.MatchedFromProfile)
	fmt.Fprintf(out, "  unmatched user goroutines: %d\n", resolution.UnmatchedHeap)
	fmt.Fprintf(out, "  unmatched profile samples: %d\n", resolution.UnmatchedProfile)
	fmt.Fprintf(out, "  ambiguous matches: %d\n", resolution.AmbiguousMatches)
	fmt.Fprintln(out)

	bySource := map[labelresolve.Source]int{}
	for _, s := range resolution.SourcesByGID {
		bySource[s]++
	}
	fmt.Fprintln(out, "label sources:")
	for _, s := range []labelresolve.Source{labelresolve.SourceHeap, labelresolve.SourceManifest, labelresolve.SourceProfileID, labelresolve.SourceProfileStack} {
		fmt.Fprintf(out, "  %s: %d\n", s, bySource[s])
	}
	if len(resolution.Warnings) > 0 {
		fmt.Fprintln(out, "label recovery warnings:")
		for _, w := range resolution.Warnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}
	fmt.Fprintln(out)

	report.PrintSummary(out)

	fmt.Fprintln(out, "bubble attribution source:")
	switch {
	case resolution.MatchedFromHeap > 0:
		fmt.Fprintln(out, "  heap.dump runtime.g.labels + fallback")
	case res.Labels != nil && prof != nil:
		fmt.Fprintln(out, "  labels.json + pprof fallback")
	case res.Labels != nil:
		fmt.Fprintln(out, "  labels.json exact")
	case prof != nil:
		fmt.Fprintln(out, "  pprof best-effort")
	default:
		fmt.Fprintln(out, "  (no label source available)")
	}
	return nil
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usage(errOut, program)
		return 2
	}
	opts, err := labelOptionsFromFlag(*labelsSource)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usage(errOut, program)
		return 2
	}
	resOpts, err := labelOptionsFromFlag(*labelsSource)
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

func labelOptionsFromFlag(src string) (labelresolve.Options, error) {
	switch src {
	case "", "auto":
		return labelresolve.Options{}, nil
	case "heap":
		return labelresolve.Options{HeapOnly: true}, nil
	case "manifest":
		return labelresolve.Options{ManifestOnly: true}, nil
	case "profile":
		return labelresolve.Options{ProfileOnly: true}, nil
	default:
		return labelresolve.Options{}, fmt.Errorf("unknown --labels-source %q", src)
	}
}

func shouldKeepHeapContents(opts labelresolve.Options) bool {
	if opts.DisableHeap || opts.ManifestOnly || opts.ProfileOnly {
		return false
	}
	return true
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
