package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

func runBubbles(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bubbles", flag.ContinueOnError)
	fs.SetOutput(stderr)
	includeSystem := fs.Bool("include-system", false, "include system/background goroutines")
	gc := fs.Bool("gc", true, "when fetching from a URL, run a garbage collection in the target before the heap dump")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout when the argument is a URL")
	target, err := parseWithOneArg(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, "bubblepprof bubbles: exactly one bundle file or target URL is required")
		fs.Usage()
		return exitUsage
	}

	b, err := loadBundle(target, *gc, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "bubblepprof bubbles: %v\n", err)
		return exitFailure
	}
	defer b.Close()

	resp, err := listBundleBubbles(context.Background(), b, memusage.Options{
		IncludeSystemGoroutines: *includeSystem,
	})
	if err != nil {
		_, body := memusage.ErrorResponseFor(err)
		writeIndentedJSON(stdout, body)
		return exitFailure
	}
	writeIndentedJSON(stdout, resp)
	return exitOK
}

func listBundleBubbles(ctx context.Context, b *bundle.Bundle, opts memusage.Options) (*memusage.BubblesResponse, error) {
	f, err := os.Open(b.HeapDumpPath)
	if err != nil {
		return nil, fmt.Errorf("open heap dump: %w", err)
	}
	defer f.Close()

	var extra addrspace.Reader
	if b.Segments != nil {
		extra = b.Segments
	}
	return memusage.ListBubbles(ctx, f, f, extra, b.Warnings, opts)
}
