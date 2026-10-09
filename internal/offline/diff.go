package offline

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/memusage"
)

type DiffOptions struct {
	By     string            `json:"by,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	A      map[string]string `json:"a,omitempty"`
	B      map[string]string `json:"b,omitempty"`
}
type Delta struct {
	Objects int64 `json:"objects"`
	// Decimal strings preserve signed differences across the full uint64 range.
	Bytes string `json:"bytes"`
}
type BucketDelta struct {
	Min uint64 `json:"min_size"`
	Max uint64 `json:"max_size"`
	Delta
}
type SummaryChange struct {
	Labels          map[string]string `json:"labels"`
	Before          *memusage.Summary `json:"before"`
	After           *memusage.Summary `json:"after"`
	GoroutinesDelta int64             `json:"goroutines_delta"`
	RootsDelta      int64             `json:"roots_delta"`
	Metrics         map[string]Delta  `json:"delta"`
	Distribution    []BucketDelta     `json:"size_distribution_delta"`
	RootCategories  map[string]int64  `json:"root_categories_delta"`
	RootFunctions   map[string]int64  `json:"root_functions_delta"`
}
type CompatibilityCheck struct {
	Field  string `json:"field"`
	Status string `json:"status"` // equal, different, unknown
	Before any    `json:"before"`
	After  any    `json:"after"`
}
type PairChange struct {
	Before *memusage.Comparison `json:"before"`
	After  *memusage.Comparison `json:"after"`
	Delta  map[string]Delta     `json:"delta"`
}
type DiffResult struct {
	SchemaVersion  int                       `json:"schema_version"`
	Selection      DiffOptions               `json:"selection"`
	Compatibility  []CompatibilityCheck      `json:"compatibility"`
	BeforeMetadata memusage.SnapshotMetadata `json:"before_metadata"`
	AfterMetadata  memusage.SnapshotMetadata `json:"after_metadata"`
	Groups         []SummaryChange           `json:"groups"`
	Pair           *PairChange               `json:"pair,omitempty"`
	Notes          []string                  `json:"notes"`
}

func measureDelta(a, b memusage.Measure) Delta {
	x := new(big.Int).SetUint64(b.Bytes)
	x.Sub(x, new(big.Int).SetUint64(a.Bytes))
	return Delta{int64(b.Objects) - int64(a.Objects), x.String()}
}
func countDelta(a, b map[string]int) map[string]int64 {
	out := map[string]int64{}
	for k, v := range a {
		out[k] = -int64(v)
	}
	for k, v := range b {
		out[k] += int64(v)
	}
	return out
}
func change(labels map[string]string, a, b *memusage.Summary) SummaryChange {
	c := SummaryChange{Labels: labels, Before: a, After: b, GoroutinesDelta: int64(b.Goroutines) - int64(a.Goroutines), RootsDelta: int64(b.Roots) - int64(a.Roots), Metrics: map[string]Delta{}, Distribution: []BucketDelta{}, RootCategories: countDelta(a.RootCategories, b.RootCategories), RootFunctions: countDelta(a.RootFunctions, b.RootFunctions)}
	for k, v := range a.Metrics {
		c.Metrics[k] = measureDelta(v, b.Metrics[k])
	}
	buckets := map[uint64]BucketDelta{}
	for _, v := range a.Distribution {
		buckets[v.Min] = BucketDelta{Min: v.Min, Max: v.Max, Delta: measureDelta(v.Measure, memusage.Measure{})}
	}
	for _, v := range b.Distribution {
		var before memusage.Measure
		for _, old := range a.Distribution {
			if old.Min == v.Min {
				before = old.Measure
				break
			}
		}
		buckets[v.Min] = BucketDelta{Min: v.Min, Max: v.Max, Delta: measureDelta(before, v.Measure)}
	}
	for _, v := range buckets {
		c.Distribution = append(c.Distribution, v)
	}
	sort.Slice(c.Distribution, func(i, j int) bool { return c.Distribution[i].Min < c.Distribution[j].Min })
	return c
}

// Diff compares independently analyzed sets, never object identities or addresses.
func Diff(ctx context.Context, before, after *Snapshot, opts DiffOptions) (*DiffResult, error) {
	modes := 0
	if opts.By != "" {
		modes++
	}
	if len(opts.Labels) > 0 {
		modes++
	}
	if len(opts.A) > 0 || len(opts.B) > 0 {
		modes++
		if len(opts.A) == 0 || len(opts.B) == 0 {
			return nil, fmt.Errorf("both A and B selectors are required")
		}
	}
	if modes != 1 {
		return nil, fmt.Errorf("choose exactly one of by, labels, or A and B selectors")
	}
	r := &DiffResult{SchemaVersion: 1, Selection: opts, BeforeMetadata: before.Metadata(), AfterMetadata: after.Metadata(), Groups: []SummaryChange{}, Notes: []string{"Objects are not matched across snapshots; deltas compare aggregate evidence.", "Growth does not prove a leak. Disappearing roots do not prove objects were reclaimed.", "Group metrics and overlap categories are not additive."}}
	check := func(field string, a, b any, known bool) {
		status := "equal"
		if !known {
			status = "unknown"
		} else if !reflect.DeepEqual(a, b) {
			status = "different"
		}
		r.Compatibility = append(r.Compatibility, CompatibilityCheck{field, status, a, b})
	}
	check("application_build", before.Capture.ApplicationBuild, after.Capture.ApplicationBuild, before.Capture.ApplicationBuild != nil && after.Capture.ApplicationBuild != nil)
	check("go_version", r.BeforeMetadata.GoVersion, r.AfterMetadata.GoVersion, r.BeforeMetadata.GoVersion != "" && r.AfterMetadata.GoVersion != "")
	check("architecture", r.BeforeMetadata.GOARCH, r.AfterMetadata.GOARCH, r.BeforeMetadata.GOARCH != "" && r.AfterMetadata.GOARCH != "")
	check("capture_settings", before.Capture.CaptureSettings, after.Capture.CaptureSettings, before.Capture.CaptureSettings != nil && after.Capture.CaptureSettings != nil)
	// Diagnostics are evidence of completeness, not a claim that every runtime root is modeled.
	completeness := func(s *Snapshot) any {
		m := s.Metadata()
		return struct {
			Validation string   `json:"validation"`
			Unresolved int      `json:"unresolved_pointers"`
			Rodata     string   `json:"rodata_status"`
			Warnings   []string `json:"warnings"`
		}{m.Validation, m.Graph.UnresolvedPointers, s.Capture.Rodata.Status, m.Warnings}
	}
	check("completeness", completeness(before), completeness(after), true)
	selectors := []map[string]string{}
	if opts.By != "" {
		values := map[string]bool{}
		for _, v := range before.LabelValues(opts.By) {
			values[v] = true
		}
		for _, v := range after.LabelValues(opts.By) {
			values[v] = true
		}
		keys := []string{}
		for v := range values {
			keys = append(keys, v)
		}
		sort.Strings(keys)
		for _, v := range keys {
			selectors = append(selectors, map[string]string{opts.By: v})
		}
	} else if len(opts.Labels) > 0 {
		selectors = append(selectors, opts.Labels)
	} else {
		selectors = append(selectors, opts.A, opts.B)
	}
	for _, labels := range selectors {
		a, e := before.Summary(ctx, memusage.Request{Labels: labels})
		if e != nil {
			return nil, e
		}
		b, e := after.Summary(ctx, memusage.Request{Labels: labels})
		if e != nil {
			return nil, e
		}
		r.Groups = append(r.Groups, change(labels, a, b))
		check("selected_roots:"+selectorKey(labels), struct{ Goroutines, Roots int }{a.Goroutines, a.Roots}, struct{ Goroutines, Roots int }{b.Goroutines, b.Roots}, true)
	}
	if len(opts.A) > 0 {
		a, e := before.Compare(ctx, memusage.Request{Labels: opts.A}, memusage.Request{Labels: opts.B})
		if e != nil {
			return nil, e
		}
		b, e := after.Compare(ctx, memusage.Request{Labels: opts.A}, memusage.Request{Labels: opts.B})
		if e != nil {
			return nil, e
		}
		r.Pair = &PairChange{a, b, map[string]Delta{"a_only": measureDelta(a.AOnly, b.AOnly), "b_only": measureDelta(a.BOnly, b.BOnly), "shared": measureDelta(a.Shared, b.Shared), "union": measureDelta(a.Union, b.Union)}}
	}
	return r, ctx.Err()
}
func selectorKey(labels map[string]string) string { b, _ := json.Marshal(labels); return string(b) }
