package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// labelsFlag accumulates -labels k=v[,k=v...] values.
type labelsFlag map[string]string

func (f labelsFlag) String() string {
	parts := make([]string, 0, len(f))
	for k, v := range f {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (f labelsFlag) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if err := f.setOne(item); err != nil {
			return err
		}
	}
	return nil
}

func (f labelsFlag) setOne(value string) error {
	k, v, ok := strings.Cut(value, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q is not of the form key=value", value)
	}
	f[k] = v
	return nil
}

// exactLabelFlag accumulates one exact -label k=v value. It exists for
// label values containing commas, which cannot be represented
// unambiguously in the comma-separated -labels form.
type exactLabelFlag struct {
	labels labelsFlag
}

func (f exactLabelFlag) String() string { return f.labels.String() }

func (f exactLabelFlag) Set(value string) error {
	return f.labels.setOne(value)
}

// runMemUsage implements
// "bubblepprof memusage <bundle-or-url> -labels k=v[,k=v...]".
func runMemUsage(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memusage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	labels := labelsFlag{}
	fs.Var(labels, "labels", "comma-separated label selector key=value[,key=value...] (repeatable)")
	fs.Var(exactLabelFlag{labels: labels}, "label", "single label selector key=value (repeat for values containing commas)")
	includeSystem := fs.Bool("include-system", false, "let system/background goroutines participate in label matching")
	gc := fs.Bool("gc", true, "when fetching from a URL, run a garbage collection in the target before the heap dump")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout when the argument is a URL")
	format := fs.String("format", "json", "output format: json or text")
	target, err := parseWithOneArg(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, "bubblepprof memusage: exactly one bundle file or target URL is required")
		fs.Usage()
		return exitUsage
	}
	if len(labels) == 0 {
		fmt.Fprintln(stderr, "bubblepprof memusage: -labels or -label is required")
		fs.Usage()
		return exitUsage
	}
	if *format != "json" && *format != "text" {
		fmt.Fprintf(stderr, "bubblepprof memusage: invalid -format %q; use json or text\n", *format)
		return exitUsage
	}

	b, err := loadBundle(target, *gc, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "bubblepprof memusage: %v\n", err)
		return exitFailure
	}
	defer b.Close()

	resp, err := analyzeBundle(context.Background(), b, memusage.Request{Labels: labels}, memusage.Options{
		IncludeSystemGoroutines: *includeSystem,
	})
	if err != nil {
		_, body := memusage.ErrorResponseFor(err)
		writeMemUsageOutput(stdout, *format, body)
		return exitFailure
	}
	writeMemUsageOutput(stdout, *format, resp)
	return exitOK
}

func loadBundle(target string, gc bool, timeout time.Duration) (*bundle.Bundle, error) {
	src, err := openBundleSource(target, gc, timeout)
	if err != nil {
		return nil, err
	}
	b, openErr := bundle.Open(src)
	closeErr := src.Close()
	if openErr != nil {
		return nil, openErr
	}
	if closeErr != nil {
		_ = b.Close()
		return nil, fmt.Errorf("close bundle source: %w", closeErr)
	}
	return b, nil
}

// analyzeBundle feeds an opened bundle into the shared analysis
// pipeline.
func analyzeBundle(ctx context.Context, b *bundle.Bundle, req memusage.Request, opts memusage.Options) (*memusage.Response, error) {
	f, err := os.Open(b.HeapDumpPath)
	if err != nil {
		return nil, fmt.Errorf("open heap dump: %w", err)
	}
	defer f.Close()

	var extra addrspace.Reader
	if b.Segments != nil {
		extra = b.Segments
	}
	return memusage.AnalyzeDump(ctx, f, f, extra, b.Warnings, req, opts)
}

// openBundleSource returns a reader over the bundle bytes: the file
// itself for a path argument, or the HTTP response body for a URL
// argument. bundle.Open consumes the source completely before analysis
// starts, so the target connection is not held during the slow analysis
// phase and there is no need to write an intermediate copy of the tar.
func openBundleSource(arg string, gc bool, timeout time.Duration) (io.ReadCloser, error) {
	// URL schemes are case-insensitive (RFC 3986).
	lower := strings.ToLower(arg)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return os.Open(arg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	body, _, err := fetchBundle(ctx, arg, gc)
	if err != nil {
		cancel()
		return nil, err
	}
	return &cancelOnClose{ReadCloser: body, cancel: cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

func writeIndentedJSON(w io.Writer, v any) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "{\"error\": %q}\n", err.Error())
		return
	}
	fmt.Fprintf(w, "%s\n", out)
}

func writeMemUsageOutput(w io.Writer, format string, v any) {
	if format == "json" {
		writeIndentedJSON(w, v)
		return
	}
	switch value := v.(type) {
	case *memusage.Response:
		writeMemUsageText(w, value)
	case *memusage.ErrorResponse:
		writeErrorText(w, value)
	default:
		fmt.Fprintf(w, "error: unsupported text output type %T\n", v)
	}
}

func writeMemUsageText(w io.Writer, resp *memusage.Response) {
	keys := make([]string, 0, len(resp.Labels))
	for key := range resp.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "labels:")
	for _, key := range keys {
		fmt.Fprintf(w, "  %s=%s\n", key, resp.Labels[key])
	}
	fmt.Fprintf(w, "matched_goroutines: %d\n", resp.MatchedGoroutines)
	fmt.Fprintf(w, "reachable_objects: %d\n", resp.ReachableObjects)
	fmt.Fprintf(w, "reachable_bytes: %d\n", resp.ReachableBytes)
	fmt.Fprintf(w, "global_overlap_objects: %d\n", resp.GlobalOverlapObjects)
	fmt.Fprintf(w, "global_overlap_bytes: %d\n", resp.GlobalOverlapBytes)
	fmt.Fprintf(w, "system_overlap_objects: %d\n", resp.SystemOverlapObjects)
	fmt.Fprintf(w, "system_overlap_bytes: %d\n", resp.SystemOverlapBytes)
}

func writeErrorText(w io.Writer, resp *memusage.ErrorResponse) {
	fmt.Fprintf(w, "error: %s\n", resp.Error)
	fmt.Fprintf(w, "code: %s\n", resp.Code)
	if resp.GoVersion != "" {
		fmt.Fprintf(w, "go_version: %s\n", resp.GoVersion)
	}
	if resp.GOARCH != "" {
		fmt.Fprintf(w, "goarch: %s\n", resp.GOARCH)
	}
	for _, warning := range resp.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}
