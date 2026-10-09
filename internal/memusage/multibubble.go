package memusage

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

type objectSet = map[snapshotgraph.ObjectID]struct{}

// Measure counts distinct objects and their recorded shallow bytes.
type Measure struct {
	Objects int    `json:"objects"`
	Bytes   uint64 `json:"bytes"`
}

// Breakdown partitions Reachable into Exclusive, BubblesOnly, OtherRootsOnly,
// and Both. Shared is the union of the latter three. Individual overlaps can
// overlap each other; they must not be summed.
type Breakdown struct {
	Reachable        Measure `json:"reachable"`
	Exclusive        Measure `json:"exclusive"`
	Shared           Measure `json:"shared"`
	BubblesOnly      Measure `json:"bubbles_only"`
	OtherRootsOnly   Measure `json:"other_roots_only"`
	Both             Measure `json:"both"`
	BubbleOverlap    Measure `json:"bubble_overlap"`
	OtherRootOverlap Measure `json:"other_root_overlap"`
	GlobalOverlap    Measure `json:"global_overlap"`
	SystemOverlap    Measure `json:"system_overlap"`
	UngroupedOverlap Measure `json:"ungrouped_overlap"`
}

type GroupResult struct {
	Value      string `json:"value"`
	Goroutines int    `json:"goroutines"`
	Breakdown
}

type TopOptions struct {
	By    string
	Sort  string // reachable (default), exclusive, shared
	Limit int    // zero means all groups
}

type TopResult struct {
	By                       string        `json:"by"`
	Groups                   []GroupResult `json:"groups"`
	TotalGroups              int           `json:"total_groups"`
	UngroupedGoroutines      int           `json:"ungrouped_goroutines"`
	ExcludedSystemGoroutines int           `json:"excluded_system_goroutines"`
}

type Comparison struct {
	A                     map[string]string `json:"a"`
	B                     map[string]string `json:"b"`
	AGoroutines           int               `json:"a_goroutines"`
	BGoroutines           int               `json:"b_goroutines"`
	OverlappingGoroutines []uint64          `json:"overlapping_goroutines"`
	AOnly                 Measure           `json:"a_only"`
	BOnly                 Measure           `json:"b_only"`
	Shared                Measure           `json:"shared"`
	Union                 Measure           `json:"union"`
}

type ExclusiveResult struct {
	Labels                map[string]string `json:"labels"`
	Goroutines            int               `json:"goroutines"`
	Reachable             Measure           `json:"reachable"`
	Exclusive             Measure           `json:"exclusive"`
	Shared                Measure           `json:"shared"`
	GlobalOverlap         Measure           `json:"global_overlap"`
	SystemOverlap         Measure           `json:"system_overlap"`
	OtherGoroutineOverlap Measure           `json:"other_goroutine_overlap"`
}

// SharedObject identifies an object within this snapshot only. Addresses are
// hexadecimal strings to avoid losing precision in JSON consumers.
type SharedObject struct {
	ID                 snapshotgraph.ObjectID `json:"id"`
	Address            string                 `json:"address"`
	Size               uint64                 `json:"size"`
	OutgoingReferences int                    `json:"outgoing_references"`
}

type SharedResult struct {
	Comparison
	Objects   []SharedObject `json:"objects"`
	Truncated bool           `json:"truncated"`
}

func (s *Snapshot) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.analysis == nil {
		return fmt.Errorf("snapshot is closed")
	}
	if s.diag.UnsupportedRuntime {
		return &UnsupportedRuntimeError{GoVersion: s.diag.GoVersion, GOARCH: s.diag.GOARCH}
	}
	known, eligible := map[uint64]struct{}{}, map[uint64]struct{}{}
	for _, g := range s.analysis.Goroutines {
		known[g.GoroutineID] = struct{}{}
		if s.opts.IncludeSystemGoroutines || !(g.IsSystem || g.IsBackground) {
			eligible[g.GoroutineID] = struct{}{}
		}
	}
	return eligibleFailureError(s.diag, known, eligible)
}

func (s *Snapshot) selected(req Request) ([]snapshotgraph.RootRef, map[uint64]struct{}, error) {
	if err := ValidateRequest(&req, s.opts); err != nil {
		return nil, nil, err
	}
	roots := []snapshotgraph.RootRef{}
	ids := map[uint64]struct{}{}
	for _, g := range s.analysis.Goroutines {
		if !s.opts.IncludeSystemGoroutines && (g.IsSystem || g.IsBackground) {
			continue
		}
		if LabelsMatch(s.labels[g.GoroutineID], req.Labels) {
			ids[g.GoroutineID] = struct{}{}
			roots = append(roots, g.Roots...)
		}
	}
	return roots, ids, nil
}

// walk is iterative, tolerates malformed IDs, and checks cancellation during
// traversal. No selector-specific sets are retained by the session.
func (s *Snapshot) walk(ctx context.Context, roots []snapshotgraph.RootRef) (objectSet, error) {
	g := s.analysis.Graph
	seen := objectSet{}
	queue := []snapshotgraph.ObjectID{}
	add := func(id snapshotgraph.ObjectID) {
		if uint64(id) >= uint64(len(g.Objects)) {
			return
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			queue = append(queue, id)
		}
	}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		add(r.ObjectID)
	}
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, id := range g.Objects[queue[head]].Children {
			add(id)
		}
	}
	return seen, ctx.Err()
}

func (s *Snapshot) measure(set objectSet) (Measure, error) {
	m := Measure{}
	for id := range set {
		size := s.analysis.Graph.Objects[id].Size
		if size > math.MaxUint64-m.Bytes {
			return Measure{}, fmt.Errorf("object byte total overflows uint64")
		}
		m.Objects++
		m.Bytes += size
	}
	return m, nil
}

func intersection(a, b objectSet) objectSet {
	out := objectSet{}
	if len(a) > len(b) {
		a, b = b, a
	}
	for id := range a {
		if _, ok := b[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out
}
func difference(a, b objectSet) objectSet {
	out := objectSet{}
	for id := range a {
		if _, ok := b[id]; !ok {
			out[id] = struct{}{}
		}
	}
	return out
}
func union(a, b objectSet) objectSet {
	out := objectSet{}
	for id := range a {
		out[id] = struct{}{}
	}
	for id := range b {
		out[id] = struct{}{}
	}
	return out
}

func (s *Snapshot) compare(ctx context.Context, a, b Request) (*Comparison, objectSet, error) {
	ar, ai, err := s.selected(a)
	if err != nil {
		return nil, nil, err
	}
	br, bi, err := s.selected(b)
	if err != nil {
		return nil, nil, err
	}
	as, err := s.walk(ctx, ar)
	if err != nil {
		return nil, nil, err
	}
	bs, err := s.walk(ctx, br)
	if err != nil {
		return nil, nil, err
	}
	shared := intersection(as, bs)
	out := &Comparison{A: copyLabels(a.Labels), B: copyLabels(b.Labels), AGoroutines: len(ai), BGoroutines: len(bi), OverlappingGoroutines: []uint64{}}
	for id := range ai {
		if _, ok := bi[id]; ok {
			out.OverlappingGoroutines = append(out.OverlappingGoroutines, id)
		}
	}
	sort.Slice(out.OverlappingGoroutines, func(i, j int) bool { return out.OverlappingGoroutines[i] < out.OverlappingGoroutines[j] })
	for _, p := range []struct {
		dst *Measure
		set objectSet
	}{{&out.AOnly, difference(as, bs)}, {&out.BOnly, difference(bs, as)}, {&out.Shared, shared}, {&out.Union, union(as, bs)}} {
		*p.dst, err = s.measure(p.set)
		if err != nil {
			return nil, nil, err
		}
	}
	return out, shared, ctx.Err()
}

// Compare computes pairwise differences, not process-wide exclusivity.
func (s *Snapshot) Compare(ctx context.Context, a, b Request) (*Comparison, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	out, _, err := s.compare(ctx, a, b)
	return out, err
}

// Shared returns the exact intersection, ordered by descending shallow size,
// then address and ID. A zero limit returns every shared object.
func (s *Snapshot) Shared(ctx context.Context, a, b Request, limit int) (*SharedResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 0 {
		return nil, fmt.Errorf("limit must be non-negative")
	}
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	c, set, err := s.compare(ctx, a, b)
	if err != nil {
		return nil, err
	}
	out := &SharedResult{Comparison: *c, Objects: []SharedObject{}}
	ids := make([]snapshotgraph.ObjectID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	objects := s.analysis.Graph.Objects
	sort.Slice(ids, func(i, j int) bool {
		a, b := objects[ids[i]], objects[ids[j]]
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		if a.Addr != b.Addr {
			return a.Addr < b.Addr
		}
		return ids[i] < ids[j]
	})
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
		out.Truncated = true
	}
	for _, id := range ids {
		o := objects[id]
		out.Objects = append(out.Objects, SharedObject{id, fmt.Sprintf("0x%x", o.Addr), o.Size, len(o.Children)})
	}
	return out, ctx.Err()
}

// Exclusive compares selected goroutine roots with globals and every
// non-selected goroutine, including excluded system/background goroutines.
func (s *Snapshot) Exclusive(ctx context.Context, req Request) (*ExclusiveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	roots, ids, err := s.selected(req)
	if err != nil {
		return nil, err
	}
	selected, err := s.walk(ctx, roots)
	if err != nil {
		return nil, err
	}
	var system, other []snapshotgraph.RootRef
	for _, g := range s.analysis.Goroutines {
		if _, ok := ids[g.GoroutineID]; ok {
			continue
		}
		if g.IsSystem || g.IsBackground {
			system = append(system, g.Roots...)
		} else {
			other = append(other, g.Roots...)
		}
	}
	global, err := s.walk(ctx, s.analysis.Globals.Roots)
	if err != nil {
		return nil, err
	}
	sys, err := s.walk(ctx, system)
	if err != nil {
		return nil, err
	}
	rest, err := s.walk(ctx, other)
	if err != nil {
		return nil, err
	}
	outside := union(global, union(sys, rest))
	out := &ExclusiveResult{Labels: copyLabels(req.Labels), Goroutines: len(ids)}
	for _, p := range []struct {
		dst *Measure
		set objectSet
	}{{&out.Reachable, selected}, {&out.Exclusive, difference(selected, outside)}, {&out.Shared, intersection(selected, outside)}, {&out.GlobalOverlap, intersection(selected, global)}, {&out.SystemOverlap, intersection(selected, sys)}, {&out.OtherGoroutineOverlap, intersection(selected, rest)}} {
		*p.dst, err = s.measure(p.set)
		if err != nil {
			return nil, err
		}
	}
	return out, ctx.Err()
}

// Top uses two walks per group and one membership counter per reached object,
// rather than retaining a bitmap for every group. Sorting/limiting only affects
// presentation: every named group contributes to classification.
func (s *Snapshot) Top(ctx context.Context, opts TopOptions) (*TopResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts.By == "" || opts.Limit < 0 {
		return nil, fmt.Errorf("by is required and limit must be non-negative")
	}
	if opts.Sort == "" {
		opts.Sort = "reachable"
	}
	if opts.Sort != "reachable" && opts.Sort != "exclusive" && opts.Sort != "shared" {
		return nil, fmt.Errorf("sort must be reachable, exclusive, or shared")
	}
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	groups := map[string][]snapshotgraph.RootRef{}
	counts := map[string]int{}
	var system, ungrouped []snapshotgraph.RootRef
	out := &TopResult{By: opts.By, Groups: []GroupResult{}}
	for _, g := range s.analysis.Goroutines {
		if !s.opts.IncludeSystemGoroutines && (g.IsSystem || g.IsBackground) {
			system = append(system, g.Roots...)
			out.ExcludedSystemGoroutines++
			continue
		}
		value, ok := s.labels[g.GoroutineID][opts.By]
		if !ok {
			ungrouped = append(ungrouped, g.Roots...)
			if g.IsSystem || g.IsBackground {
				system = append(system, g.Roots...)
			}
			out.UngroupedGoroutines++
			continue
		}
		counts[value]++
		groups[value] = append(groups[value], g.Roots...)
	}
	global, err := s.walk(ctx, s.analysis.Globals.Roots)
	if err != nil {
		return nil, err
	}
	sys, err := s.walk(ctx, system)
	if err != nil {
		return nil, err
	}
	rest, err := s.walk(ctx, ungrouped)
	if err != nil {
		return nil, err
	}
	outside := union(global, union(sys, rest))
	membership := map[snapshotgraph.ObjectID]int{}
	for _, roots := range groups {
		set, e := s.walk(ctx, roots)
		if e != nil {
			return nil, e
		}
		for id := range set {
			membership[id]++
		}
	}
	for value, roots := range groups {
		set, e := s.walk(ctx, roots)
		if e != nil {
			return nil, e
		}
		bubblesOnly, otherOnly, both, exclusive := objectSet{}, objectSet{}, objectSet{}, objectSet{}
		for id := range set {
			_, o := outside[id]
			b := membership[id] > 1
			switch {
			case b && o:
				both[id] = struct{}{}
			case b:
				bubblesOnly[id] = struct{}{}
			case o:
				otherOnly[id] = struct{}{}
			default:
				exclusive[id] = struct{}{}
			}
		}
		row := GroupResult{Value: value, Goroutines: counts[value]}
		for _, p := range []struct {
			dst *Measure
			set objectSet
		}{{&row.Reachable, set}, {&row.Exclusive, exclusive}, {&row.Shared, difference(set, exclusive)}, {&row.BubblesOnly, bubblesOnly}, {&row.OtherRootsOnly, otherOnly}, {&row.Both, both}, {&row.BubbleOverlap, union(bubblesOnly, both)}, {&row.OtherRootOverlap, union(otherOnly, both)}, {&row.GlobalOverlap, intersection(set, global)}, {&row.SystemOverlap, intersection(set, sys)}, {&row.UngroupedOverlap, intersection(set, rest)}} {
			*p.dst, err = s.measure(p.set)
			if err != nil {
				return nil, err
			}
		}
		out.Groups = append(out.Groups, row)
	}
	out.TotalGroups = len(out.Groups)
	metric := func(g GroupResult) uint64 {
		switch opts.Sort {
		case "exclusive":
			return g.Exclusive.Bytes
		case "shared":
			return g.Shared.Bytes
		default:
			return g.Reachable.Bytes
		}
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if metric(a) != metric(b) {
			return metric(a) > metric(b)
		}
		return a.Value < b.Value
	})
	if opts.Limit > 0 && len(out.Groups) > opts.Limit {
		out.Groups = out.Groups[:opts.Limit]
	}
	return out, ctx.Err()
}
