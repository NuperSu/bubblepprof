package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "pprof", "pprof or json")
	mode := fs.String("pprof-mode", "summary", "pprof hierarchy: summary or paths")
	depth := fs.Int("max-path-depth", memusage.DefaultPprofPathDepth, "maximum object frames in paths mode (0 for full paths)")
	output := fs.String("o", "-", "output path (- for stdout)")
	include := fs.Bool("include-system", false, "include system/background goroutines in selection")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	labels := labelsFlag{}
	fs.Var(labels, "labels", "selector key=value[,key=value...]")
	fs.Var(exactLabelFlag{labels}, "label", "one exact key=value pair")
	target, err := parseWithOneArg(fs, args)
	if err == flag.ErrHelp {
		return exitOK
	}
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max, KeepReferenceProvenance: *format == "pprof" && *mode == "paths"}
	if err == nil {
		if e := memusage.ValidateRequest(&memusage.Request{Labels: labels}, opts); e != nil {
			err = e
		}
	}
	if err == nil && *format != "pprof" && *format != "json" {
		err = fmt.Errorf("format must be pprof or json")
	}
	if err == nil && *mode != "summary" && *mode != "paths" {
		err = fmt.Errorf("pprof mode must be summary or paths")
	}
	if err == nil && (*depth < 0 || *depth == 1) {
		err = fmt.Errorf("max path depth must be 0 or at least 2")
	}
	fs.Visit(func(f *flag.Flag) {
		if err == nil && f.Name == "pprof-mode" && *format != "pprof" {
			err = fmt.Errorf("pprof mode requires pprof format")
		}
		if err == nil && f.Name == "max-path-depth" && (*format != "pprof" || *mode != "paths") {
			err = fmt.Errorf("max path depth requires pprof paths mode")
		}
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	s, err := offline.OpenSnapshot(ctx, target, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer s.Close()
	// Prepare output before opening a destination so analysis errors don't truncate it.
	var write func(io.Writer) error
	if *format == "pprof" {
		pprofOpts := memusage.PprofOptions{Mode: *mode}
		if *mode == "paths" {
			pprofOpts.MaxPathDepth = *depth
		}
		p, e := s.ExportPprof(ctx, memusage.Request{Labels: labels}, pprofOpts)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return exitFailure
		}
		if t, e := time.Parse(time.RFC3339Nano, s.Capture.HeapDumpStartedAt); e == nil {
			p.TimeNanos = t.UnixNano()
		} else if t, e := time.Parse(time.RFC3339, s.Capture.CreatedAt); e == nil {
			p.TimeNanos = t.UnixNano()
		}
		write = p.Write
	} else {
		r, e := s.Export(ctx, memusage.Request{Labels: labels})
		if e != nil {
			fmt.Fprintln(stderr, e)
			return exitFailure
		}
		write = func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				Capture any `json:"capture"`
				*memusage.ExportReport
			}{s.Capture, r})
		}
	}
	w := stdout
	if *output != "-" {
		f, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return exitFailure
		}
		if e = write(f); e != nil {
			f.Close()
			fmt.Fprintln(stderr, e)
			return exitFailure
		}
		if e = f.Close(); e != nil {
			fmt.Fprintln(stderr, e)
			return exitFailure
		}
	} else if e := write(w); e != nil {
		fmt.Fprintln(stderr, e)
		return exitFailure
	}
	return exitOK
}
