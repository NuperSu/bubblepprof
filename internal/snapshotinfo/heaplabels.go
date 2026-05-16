package snapshotinfo

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/runtimelayout"
	"bubblepprof/internal/snapshotparse"
)

// heapLabelCLIOptions mirrors the user-facing flags of the
// `snapshot heap-labels` debug command. Only one of ManualOffset and the
// automatic table lookup is used per run; --find-offset is a separate
// discovery path that may seed ManualOffset when unambiguous.
type heapLabelCLIOptions struct {
	DecodeOptions heaplabels.Options

	ManualOffset    uint64
	HasManualOffset bool

	FindLabels map[string]string
	ShowFailed bool
}

const (
	layoutSourceTable     = "table"
	layoutSourceManual    = "manual"
	layoutSourceDebugScan = "debug-scan"
	layoutSourceNone      = "none"
)

func PrintHeapLabels(out io.Writer, path string, cli heapLabelCLIOptions) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	res, err := snapshotparse.ParseSnapshot(f, heapdump.Options{KeepObjectContents: true})
	if err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}

	snap := res.Snapshot
	fmt.Fprintf(out, "snapshot format: %s\n", res.Metadata.Format)
	if res.Metadata.GoVersion != "" {
		fmt.Fprintf(out, "metadata go version: %s\n", res.Metadata.GoVersion)
	}
	fmt.Fprintf(out, "heap dump build version: %s\n", snap.Params.BuildVersion)
	fmt.Fprintf(out, "goarch: %s\n", snap.Params.GOARCH)
	fmt.Fprintf(out, "ptr size: %d\n", snap.Params.PtrSize)
	fmt.Fprintln(out)

	input := heaplabels.LookupInputFromSnapshot(snap)

	// --find-offset is a debug-scan path. When a single unambiguous
	// candidate is found and no manual offset was supplied, the CLI may
	// use it for the subsequent decode, but the layout source is reported
	// as debug-scan, not as verified.
	var (
		scanOffset    uint64
		haveScanMatch bool
	)
	if len(cli.FindLabels) > 0 {
		mem := heaplabels.NewMemory(snap)
		candidates := heaplabels.FindOffsetCandidates(snap, mem, cli.FindLabels, cli.DecodeOptions)
		fmt.Fprintln(out, "offset discovery:")
		fmt.Fprintf(out, "  expected labels: %s\n", strings.Join(heaplabels.FormatLabels(cli.FindLabels), ", "))
		fmt.Fprintf(out, "  candidates: %d\n", len(candidates))
		for _, c := range candidates {
			fmt.Fprintf(out, "  runtime.g.labels offset: 0x%x (matches: %d, goroutines: %v)\n",
				c.Offset, c.Matches, c.GoroutineIDs)
		}
		if !cli.HasManualOffset && len(candidates) == 1 {
			scanOffset, haveScanMatch = candidates[0].Offset, true
			fmt.Fprintf(out, "  using discovered offset: 0x%x\n", scanOffset)
		} else if !cli.HasManualOffset && len(candidates) > 1 {
			fmt.Fprintln(out, "  ambiguous offset discovery; provide --g-labels-offset to decode")
		}
		fmt.Fprintln(out)
	}

	layout, layoutSource, ok := resolveCLILayout(input, cli, scanOffset, haveScanMatch)

	fmt.Fprintf(out, "layout source: %s\n", layoutSource)
	if input.GoVersion != "" {
		fmt.Fprintf(out, "go version: %s\n", input.GoVersion)
	}
	fmt.Fprintf(out, "goarch: %s\n", input.GOARCH)
	fmt.Fprintf(out, "ptr size: %d\n", input.PtrSize)
	if ok {
		fmt.Fprintf(out, "runtime.g.labels offset: 0x%x\n", layout.GLabelsOffset)
		if layout.Description != "" {
			fmt.Fprintf(out, "layout description: %s\n", layout.Description)
		}
	} else {
		fmt.Fprintln(out, "runtime.g.labels offset: (not configured)")
		fmt.Fprintf(out, "diagnostic: %s\n", runtimelayout.UnsupportedMessage(input))
	}
	fmt.Fprintln(out)

	var decoded heaplabels.Result
	if ok {
		decoded = heaplabels.DecodeAll(snap, layout, cli.DecodeOptions)
	} else {
		decoded = heaplabels.UnsupportedResult(snap, runtimelayout.UnsupportedMessage(input))
	}
	decoded.PrintSummary(out)

	printGoroutineHeapLabels(out, decoded, cli.ShowFailed)
	if len(decoded.Warnings) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "warnings:")
		for _, w := range decoded.Warnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}
	return nil
}

// resolveCLILayout picks the runtime layout for `snapshot heap-labels`.
// Resolution order:
//
//  1. --g-labels-offset (manual) — debug-only, never used by
//     /debug/memusage.
//  2. --find-offset single candidate — debug-scan source.
//  3. runtimelayout.Lookup — verified table.
//
// When all three fail, the function returns ok=false so callers can emit
// an unsupported_runtime diagnostic instead of decoding with a guess.
func resolveCLILayout(
	input runtimelayout.LookupInput,
	cli heapLabelCLIOptions,
	scanOffset uint64,
	haveScanMatch bool,
) (runtimelayout.Layout, string, bool) {
	if cli.HasManualOffset {
		layout, err := runtimelayout.Manual(input, cli.ManualOffset)
		if err != nil {
			return runtimelayout.Layout{}, layoutSourceNone, false
		}
		return layout, layoutSourceManual, true
	}
	if haveScanMatch {
		layout, err := runtimelayout.Manual(input, scanOffset)
		if err != nil {
			return runtimelayout.Layout{}, layoutSourceNone, false
		}
		return layout, layoutSourceDebugScan, true
	}
	if layout, ok := runtimelayout.Lookup(input); ok {
		return layout, layoutSourceTable, true
	}
	return runtimelayout.Layout{}, layoutSourceNone, false
}

func printGoroutineHeapLabels(out io.Writer, res heaplabels.Result, showFailed bool) {
	decoded := make([]heaplabels.GoroutineResult, 0)
	other := make([]heaplabels.GoroutineResult, 0)
	for _, gr := range res.Goroutines {
		if gr.Status == heaplabels.StatusDecoded && len(gr.Labels) > 0 {
			decoded = append(decoded, gr)
		} else if showFailed && gr.Status != heaplabels.StatusNoLabels {
			other = append(other, gr)
		}
	}
	sort.Slice(decoded, func(i, j int) bool { return decoded[i].GID < decoded[j].GID })
	sort.Slice(other, func(i, j int) bool { return other[i].GID < other[j].GID })

	if len(decoded) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "decoded goroutine labels:")
		for _, gr := range decoded {
			fmt.Fprintf(out, "  goroutine %d:\n", gr.GID)
			for _, kv := range heaplabels.FormatLabels(gr.Labels) {
				fmt.Fprintf(out, "    %s\n", kv)
			}
		}
	}

	if len(other) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "non-decoded goroutines:")
		for _, gr := range other {
			fmt.Fprintf(out, "  goroutine %d: %s", gr.GID, gr.Status)
			if gr.Error != "" {
				fmt.Fprintf(out, " (%s)", gr.Error)
			}
			fmt.Fprintln(out)
		}
	}
}

func runHeapLabels(out, errOut io.Writer, program string, args []string) int {
	fs := flag.NewFlagSet("snapshot heap-labels", flag.ContinueOnError)
	fs.SetOutput(errOut)
	offsetText := fs.String("g-labels-offset", "", "runtime.g.labels offset, decimal or 0x-prefixed hex (debug only)")
	findOffset := fs.String("find-offset", "", "expected labels to scan for, e.g. bubble=alpha or bubble=alpha,job=42")
	maxLabels := fs.Uint64("max-labels", heaplabels.DefaultMaxLabels, "maximum labels accepted in one pprof label map")
	maxString := fs.Uint64("max-string", heaplabels.DefaultMaxStringLen, "maximum decoded label string length")
	showFailed := fs.Bool("show-failed", false, "show non-decoded goroutine statuses")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usage(errOut, program)
		return 2
	}

	cli := heapLabelCLIOptions{
		DecodeOptions: heaplabels.Options{
			MaxLabels:    *maxLabels,
			MaxStringLen: *maxString,
		},
		ShowFailed: *showFailed,
	}
	if *offsetText != "" {
		off, err := strconv.ParseUint(*offsetText, 0, 64)
		if err != nil {
			fmt.Fprintf(errOut, "invalid --g-labels-offset %q: %v\n", *offsetText, err)
			return 2
		}
		cli.ManualOffset = off
		cli.HasManualOffset = true
	}
	findLabels, err := parseFindLabels(*findOffset)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	cli.FindLabels = findLabels

	if err := PrintHeapLabels(out, rest[0], cli); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func parseFindLabels(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid --find-offset label %q; expected key=value", part)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid --find-offset %q", s)
	}
	return out, nil
}
