package memusage

import (
	"errors"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

func TestListBubblesFromDecodedGroupsExactLabelSets(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 1},
			{ID: 2},
			{ID: 3},
			{ID: 4, IsSystem: true},
			{ID: 5},
		},
	}
	result := heaplabels.Result{
		LabelsByGID: map[uint64]map[string]string{
			1: {"tenant": "acme", "job": "checkout"},
			2: {"job": "checkout", "tenant": "acme"},
			3: {"tenant": "beta"},
			4: {"tenant": "system"},
		},
	}

	resp, err := listBubblesFromDecoded(snap, result, Diagnostics{}, Options{})
	if err != nil {
		t.Fatalf("listBubblesFromDecoded: %v", err)
	}
	if len(resp.Bubbles) != 2 {
		t.Fatalf("bubbles = %+v", resp.Bubbles)
	}
	if got := resp.Bubbles[0]; got.GoroutineCount != 2 || got.Labels["job"] != "checkout" || got.Labels["tenant"] != "acme" {
		t.Fatalf("first bubble = %+v", got)
	}
	if got := resp.Bubbles[1]; got.GoroutineCount != 1 || got.Labels["tenant"] != "beta" {
		t.Fatalf("second bubble = %+v", got)
	}

	resp, err = listBubblesFromDecoded(snap, result, Diagnostics{}, Options{IncludeSystemGoroutines: true})
	if err != nil {
		t.Fatalf("include system: %v", err)
	}
	if len(resp.Bubbles) != 3 || resp.Bubbles[2].Labels["tenant"] != "system" {
		t.Fatalf("include-system bubbles = %+v", resp.Bubbles)
	}
}

func TestListBubblesFromDecodedEligibleFailures(t *testing.T) {
	snap := &heapsnapshot.HeapSnapshot{
		Goroutines: []heapsnapshot.Goroutine{
			{ID: 1},
			{ID: 2, IsSystem: true},
		},
	}
	diag := Diagnostics{
		FailedGoroutines:   1,
		StringMissingCount: 1,
		FailedGIDs:         map[uint64]struct{}{2: {}},
		StringMissingGIDs:  map[uint64]struct{}{2: {}},
	}

	if _, err := listBubblesFromDecoded(snap, heaplabels.Result{}, diag, Options{}); err != nil {
		t.Fatalf("excluded system failure should be ignored: %v", err)
	}
	_, err := listBubblesFromDecoded(snap, heaplabels.Result{}, diag, Options{IncludeSystemGoroutines: true})
	var stringMissing *StringMissingError
	if !errors.As(err, &stringMissing) {
		t.Fatalf("error = %v, want StringMissingError", err)
	}
}
