package cli

import (
	"bufio"
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

// explore accepts a JSON array of ordinary memusage requests and evaluates
// each against one loaded graph. An empty batch is useful for inspection.
func runExplore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	queries := fs.String("queries", "", "JSON file containing an array of label query requests")
	include := fs.Bool("include-system", false, "include system/background goroutines")
	max := fs.Uint64("max-mem-range-bytes", 0, "maximum bytes per heap memory record")
	target, err := parseWithOneArg(fs, args)
	if err != nil || *queries == "" {
		fmt.Fprintln(stderr, "usage: bubblepprof explore <bundle-file-or-url> -queries queries.json")
		return exitUsage
	}
	f, err := os.Open(*queries)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	var requests []memusage.Request
	dec := json.NewDecoder(bufio.NewReader(f))
	err = dec.Decode(&requests)
	if err == nil {
		var trailing any
		if e := dec.Decode(&trailing); e != io.EOF {
			err = fmt.Errorf("unexpected trailing JSON")
		}
	}
	f.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	opts := memusage.Options{IncludeSystemGoroutines: *include, MaxMemRangeBytes: *max}
	for i := range requests {
		if err := memusage.ValidateRequest(&requests[i], opts); err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
	}
	b, err := loadBundle(target, true, 5*time.Minute)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer b.Close()
	s, err := offline.LoadBundle(context.Background(), b, opts)
	if err != nil {
		_, body := memusage.ErrorResponseFor(err)
		writeIndentedJSON(stdout, body)
		return exitFailure
	}
	defer s.Close()
	type labelGroup struct {
		Key        string   `json:"key"`
		Value      string   `json:"value"`
		Goroutines []uint64 `json:"goroutines"`
	}
	groups := []labelGroup{}
	for _, k := range s.LabelKeys() {
		for _, v := range s.LabelValues(k) {
			groups = append(groups, labelGroup{k, v, s.GoroutineIDs(k, v)})
		}
	}
	results := []any{}
	code := exitOK
	for _, req := range requests {
		resp, err := s.Query(context.Background(), req)
		if err != nil {
			_, body := memusage.ErrorResponseFor(err)
			results = append(results, body)
			code = exitFailure
		} else {
			results = append(results, resp)
		}
	}
	writeIndentedJSON(stdout, struct {
		Capture  any          `json:"capture"`
		Metadata any          `json:"metadata"`
		Labels   []labelGroup `json:"labels"`
		Results  []any        `json:"results"`
	}{s.Capture, s.Metadata(), groups, results})
	return code
}
