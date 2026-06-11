package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// labelsFlag accumulates -labels k=v[,k=v...] values; the flag is
// repeatable so values containing commas can be passed one per flag.
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
		k, v, ok := strings.Cut(item, "=")
		if !ok || k == "" {
			return fmt.Errorf("label %q is not of the form key=value", item)
		}
		f[k] = v
	}
	return nil
}

// runMemUsage implements
// "bubblepprof memusage <bundle-or-url> -labels k=v[,k=v...]".
func runMemUsage(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memusage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	labels := labelsFlag{}
	fs.Var(labels, "labels", "label selector key=value[,key=value...] (repeatable)")
	includeSystem := fs.Bool("include-system", false, "let system/background goroutines participate in label matching")
	gc := fs.Bool("gc", true, "when fetching from a URL, run a garbage collection in the target before the heap dump")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout when the argument is a URL")
	target, err := parseWithOneArg(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, "bubblepprof memusage: exactly one bundle file or target URL is required")
		fs.Usage()
		return exitUsage
	}
	if len(labels) == 0 {
		fmt.Fprintln(stderr, "bubblepprof memusage: -labels is required")
		fs.Usage()
		return exitUsage
	}

	src, err := openBundleSource(target, *gc, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "bubblepprof memusage: %v\n", err)
		return exitFailure
	}
	defer src.Close()

	b, err := bundle.Open(src)
	if err != nil {
		fmt.Fprintf(stderr, "bubblepprof memusage: %v\n", err)
		return exitFailure
	}
	defer b.Close()

	resp, err := analyzeBundle(context.Background(), b, memusage.Request{Labels: labels}, memusage.Options{
		IncludeSystemGoroutines: *includeSystem,
	})
	if err != nil {
		// Same code mapping as the in-process endpoint, printed as the
		// endpoint's JSON error body.
		_, body := memusage.ErrorResponseFor(err)
		writeIndentedJSON(stdout, body)
		return exitFailure
	}
	writeIndentedJSON(stdout, resp)
	return exitOK
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
// itself for a path argument, or a temp-file-backed download for a URL
// argument (downloaded fully first so a slow analysis cannot hold the
// HTTP connection open).
func openBundleSource(arg string, gc bool, timeout time.Duration) (io.ReadCloser, error) {
	// URL schemes are case-insensitive (RFC 3986).
	lower := strings.ToLower(arg)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return os.Open(arg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	body, _, err := fetchBundle(ctx, arg, gc)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	tmp, err := os.CreateTemp("", "bubblepprof-fetch-*.tar")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("download bundle: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return &removeOnClose{File: tmp}, nil
}

// removeOnClose deletes the temp file when the download is closed.
type removeOnClose struct {
	*os.File
}

func (r *removeOnClose) Close() error {
	err := r.File.Close()
	_ = os.Remove(r.File.Name())
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
