package memusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// Bubble is one exact pprof label set and the number of goroutines carrying it.
type Bubble struct {
	Labels         map[string]string `json:"labels"`
	GoroutineCount int               `json:"goroutine_count"`
}

// BubblesResponse is the result of listing exact label sets in a dump.
type BubblesResponse struct {
	Bubbles []Bubble `json:"bubbles"`
}

// ListBubbles parses a heap dump and recovers goroutine pprof labels without
// building the object graph or running reachability. System/background
// goroutines are excluded unless opts.IncludeSystemGoroutines is set.
func ListBubbles(
	ctx context.Context,
	r io.Reader,
	ra io.ReaderAt,
	extra addrspace.Reader,
	extraWarnings []string,
	opts Options,
) (*BubblesResponse, error) {
	snap, result, diag, err := parseAndRecoverLabels(
		ctx, r, ra, DefaultLabelRecoverer{}, extra, extraWarnings,
	)
	if err != nil {
		return nil, err
	}
	return listBubblesFromDecoded(snap, result, diag, opts)
}

func listBubblesFromDecoded(
	snap *heapsnapshot.HeapSnapshot,
	result heaplabels.Result,
	diag Diagnostics,
	opts Options,
) (*BubblesResponse, error) {
	if snap == nil {
		return nil, fmt.Errorf("memusage: heap snapshot is nil")
	}
	if diag.UnsupportedRuntime {
		return nil, &UnsupportedRuntimeError{GoVersion: diag.GoVersion, GOARCH: diag.GOARCH}
	}

	knownGIDs := make(map[uint64]struct{}, len(snap.Goroutines))
	eligibleGIDs := make(map[uint64]struct{}, len(snap.Goroutines))
	for _, g := range snap.Goroutines {
		knownGIDs[g.ID] = struct{}{}
		if !opts.IncludeSystemGoroutines && (g.IsSystem || g.IsBackground) {
			continue
		}
		eligibleGIDs[g.ID] = struct{}{}
	}
	if err := eligibleFailureError(diag, knownGIDs, eligibleGIDs); err != nil {
		return nil, err
	}

	groups := make(map[string]*Bubble)
	for _, g := range snap.Goroutines {
		if _, ok := eligibleGIDs[g.ID]; !ok {
			continue
		}
		labels := result.LabelsByGID[g.ID]
		if len(labels) == 0 {
			continue
		}
		keyBytes, err := json.Marshal(labels)
		if err != nil {
			return nil, err
		}
		key := string(keyBytes)
		group := groups[key]
		if group == nil {
			group = &Bubble{Labels: copyLabels(labels)}
			groups[key] = group
		}
		group.GoroutineCount++
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	resp := &BubblesResponse{Bubbles: make([]Bubble, 0, len(keys))}
	for _, key := range keys {
		resp.Bubbles = append(resp.Bubbles, *groups[key])
	}
	return resp, nil
}
