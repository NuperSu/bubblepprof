package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "text or json")
	gc := fs.Bool("gc", true, "run GC when fetching a capture")
	timeout := fs.Duration("timeout", 5*time.Minute, "total fetch timeout")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	target, err := parseWithOneArg(fs, args)
	if err != nil || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "inspect requires a bundle file or URL; format must be text or json")
		return exitUsage
	}
	b, err := loadBundle(target, *gc, *timeout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer b.Close()
	s, err := offline.LoadBundle(context.Background(), b, memusage.Options{MaxMemRangeBytes: *max})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer s.Close()
	r := struct {
		Capture   any                       `json:"capture"`
		Metadata  memusage.SnapshotMetadata `json:"metadata"`
		LabelKeys []string                  `json:"label_keys"`
	}{s.Capture, s.Metadata(), s.LabelKeys()}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		_, err = fmt.Fprintf(stdout, "Go: %s; architecture: %s; captured: %s\nproducer: %s; validation: %s\nobjects: %d; object bytes: %d; edges: %d\ngoroutines: %d; recovered labeled goroutines: %d\nunresolved pointers: %d; label keys: %q\n", r.Metadata.GoVersion, r.Metadata.GOARCH, s.Capture.CreatedAt, s.Capture.Producer, r.Metadata.Validation, r.Metadata.Graph.Objects, r.Metadata.Graph.ObjectBytes, r.Metadata.Graph.Edges, r.Metadata.Graph.Goroutines, r.Metadata.RecoveredGoroutines, r.Metadata.Graph.UnresolvedPointers, r.LabelKeys)
		fmt.Fprintf(stdout, "application build: %v\ncapture settings: %v\nstandard profiles: %v\n", s.Capture.ApplicationBuild, s.Capture.CaptureSettings, s.Capture.Profiles)
		for _, w := range r.Metadata.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", w)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return exitOK
}
