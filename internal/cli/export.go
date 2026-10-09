package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "pprof", "pprof or json")
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
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max}
	if err == nil {
		if e := memusage.ValidateRequest(&memusage.Request{Labels: labels}, opts); e != nil {
			err = e
		}
	}
	if err == nil && *format != "pprof" && *format != "json" {
		err = fmt.Errorf("format must be pprof or json")
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	s, err := offline.OpenSnapshot(context.Background(), target, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer s.Close()
	r, err := s.Export(context.Background(), memusage.Request{Labels: labels})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	// Prepare output before opening a destination so analysis errors don't truncate it.
	var write func(io.Writer) error
	if *format == "pprof" {
		p, e := r.Pprof()
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
