package memusage

import (
	"context"
	"fmt"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

type ExplainOptions struct {
	By         string
	MaxGroups  int // 0 defaults to 20 membership values
	MaxPaths   int // 0 defaults to 6 witnesses per object
	MaxSteps   int // 0 defaults to 64 objects per witness
	MaxVisited int // 0 allows V objects per BFS; exhaustion is explicit
}

type SelectionReachability struct {
	Labels     map[string]string `json:"labels"`
	Goroutines int               `json:"goroutines"`
	Reachable  bool              `json:"reachable"`
}

type FrameWitness struct {
	SP         string `json:"sp"`
	Depth      uint64 `json:"depth"`
	EntryPC    string `json:"entry_pc"`
	CurrentPC  string `json:"current_pc"`
	ContinuePC string `json:"continue_pc"`
}

type RootWitness struct {
	Category    string        `json:"category"`
	GoroutineID uint64        `json:"goroutine_id,omitempty"`
	Kind        string        `json:"kind"`
	Pointer     string        `json:"pointer,omitempty"`
	Slot        string        `json:"slot,omitempty"`
	Detail      string        `json:"detail,omitempty"`
	Frame       *FrameWitness `json:"frame,omitempty"`
}

type PathStep struct {
	SharedObject
	// IncomingOffset is the slot offset in the preceding allocation. Nil means
	// no retained slot information (or the first, root-referenced object).
	IncomingOffset  *uint64 `json:"incoming_offset,omitempty"`
	IncomingPointer string  `json:"incoming_pointer,omitempty"`
}

type PathReport struct {
	Category   string            `json:"category"`
	Labels     map[string]string `json:"labels,omitempty"`
	Status     string            `json:"status"` // found, search_limit, not_reachable
	Root       *RootWitness      `json:"root,omitempty"`
	Steps      []PathStep        `json:"steps"`
	TotalSteps int               `json:"total_steps"`
	Truncated  bool              `json:"truncated"`
}

type ObjectExplanation struct {
	Object           ObjectInfo              `json:"object"`
	By               string                  `json:"by,omitempty"`
	Selections       []SelectionReachability `json:"selections"`
	ReachableSources int                     `json:"reachable_sources"`
	Paths            []PathReport            `json:"paths"`
	PathsTruncated   bool                    `json:"paths_truncated"`
}

type AggregateExplanation struct {
	ObjectsResult
	LargestShared            []ObjectInfo        `json:"largest_shared"`
	LargestExclusive         []ObjectInfo        `json:"largest_exclusive"`
	SharedTruncated          bool                `json:"shared_truncated"`
	ExclusiveTruncated       bool                `json:"exclusive_truncated"`
	Explanations             []ObjectExplanation `json:"explanations"`
	RepresentativesTruncated bool                `json:"representatives_truncated"`
}

type pathSource struct {
	category string
	labels   map[string]string
	value    string
	roots    []snapshotgraph.RootRef
	gids     []uint64 // parallel to roots for goroutine sources
}

func (o ExplainOptions) normalized() (ExplainOptions, error) {
	if o.MaxGroups < 0 || o.MaxPaths < 0 || o.MaxSteps < 0 || o.MaxVisited < 0 {
		return o, fmt.Errorf("explanation limits must be non-negative")
	}
	if o.MaxGroups == 0 {
		o.MaxGroups = 20
	}
	if o.MaxPaths == 0 {
		o.MaxPaths = 6
	}
	if o.MaxSteps == 0 {
		o.MaxSteps = 64
	}
	return o, nil
}

func (s *Snapshot) selectionSource(req Request) (pathSource, int, error) {
	_, ids, err := s.selected(req)
	if err != nil {
		return pathSource{}, 0, err
	}
	source := pathSource{category: "selection", labels: copyLabels(req.Labels)}
	for _, g := range s.analysis.Goroutines {
		if _, ok := ids[g.GoroutineID]; ok {
			for _, r := range g.Roots {
				source.roots = append(source.roots, r)
				source.gids = append(source.gids, g.GoroutineID)
			}
		}
	}
	return source, len(ids), nil
}

func hexAddress(n uint64) string { return fmt.Sprintf("0x%x", n) }

func (s *Snapshot) path(ctx context.Context, source pathSource, target snapshotgraph.ObjectID, opts ExplainOptions) (PathReport, error) {
	out := PathReport{Category: source.category, Labels: copyLabels(source.labels), Status: "not_reachable", Steps: []PathStep{}}
	w, err := snapshotgraph.FindPath(ctx, s.analysis.Graph, source.roots, target, opts.MaxVisited)
	if err != nil {
		return out, err
	}
	if w.SearchLimited {
		out.Status = "search_limit"
		return out, nil
	}
	if !w.Found {
		return out, nil
	}
	out.Status = "found"
	r := source.roots[w.RootIndex]
	root := &RootWitness{Category: source.category, Kind: r.Kind, Detail: r.Detail}
	if w.RootIndex < len(source.gids) {
		root.GoroutineID = source.gids[w.RootIndex]
	}
	if r.Ptr != 0 {
		root.Pointer = hexAddress(r.Ptr)
	}
	if r.SlotAddr != 0 {
		root.Slot = hexAddress(r.SlotAddr)
	}
	if r.Frame != nil {
		f := r.Frame
		root.Frame = &FrameWitness{hexAddress(f.SP), f.Depth, hexAddress(f.EntryPC), hexAddress(f.CurrentPC), hexAddress(f.ContinuePC)}
	}
	out.Root = root
	out.TotalSteps = len(w.Objects)
	ids := w.Objects
	if len(ids) > opts.MaxSteps {
		ids = ids[:opts.MaxSteps]
		out.Truncated = true
	}
	for i, id := range ids {
		o := s.analysis.Graph.Objects[id]
		step := PathStep{SharedObject: SharedObject{id, hexAddress(o.Addr), o.Size, len(o.Children)}}
		if i > 0 {
			r := w.References[i-1]
			if r.OffsetKnown {
				offset := r.Offset
				step.IncomingOffset = &offset
			}
			if r.Pointer != 0 {
				step.IncomingPointer = hexAddress(r.Pointer)
			}
		}
		out.Steps = append(out.Steps, step)
	}
	return out, ctx.Err()
}

func (s *Snapshot) explainObject(ctx context.Context, id snapshotgraph.ObjectID, selectors []Request, in *inspection, opts ExplainOptions) (*ObjectExplanation, error) {
	out := &ObjectExplanation{Object: s.objectInfo(id, in), By: opts.By, Selections: []SelectionReachability{}, Paths: []PathReport{}}
	sources := []pathSource{}
	for _, req := range selectors {
		source, count, err := s.selectionSource(req)
		if err != nil {
			return nil, err
		}
		set, err := s.walk(ctx, source.roots)
		if err != nil {
			return nil, err
		}
		_, reached := set[id]
		out.Selections = append(out.Selections, SelectionReachability{copyLabels(req.Labels), count, reached})
		if reached {
			sources = append(sources, source)
		}
	}
	global := pathSource{category: "global", roots: s.analysis.Globals.Roots}
	system, other := pathSource{category: "other_system"}, pathSource{category: "other_goroutines"}
	for _, g := range s.analysis.Goroutines {
		if _, ok := in.gids[g.GoroutineID]; ok {
			continue
		}
		source := &other
		if g.IsSystem || g.IsBackground {
			source = &system
		}
		for _, r := range g.Roots {
			source.roots = append(source.roots, r)
			source.gids = append(source.gids, g.GoroutineID)
		}
	}
	for _, source := range []pathSource{global, system, other} {
		reached := false
		switch source.category {
		case "global":
			reached = out.Object.Global
		case "other_system":
			reached = out.Object.OtherSystem
		case "other_goroutines":
			reached = out.Object.OtherGoroutines
		}
		if reached {
			sources = append(sources, source)
		}
	}
	if opts.By != "" {
		for _, group := range s.groupSources(opts.By) {
			set, err := s.walk(ctx, group.roots)
			if err != nil {
				return nil, err
			}
			if _, ok := set[id]; !ok {
				continue
			}
			out.Object.GroupCount++
			if len(out.Object.Groups) < opts.MaxGroups {
				out.Object.Groups = append(out.Object.Groups, group.value)
			} else {
				out.Object.GroupsTruncated = true
			}
			// Only retain sources whose paths can be printed, while counting all memberships.
			if len(sources) < opts.MaxPaths {
				sources = append(sources, group)
			} else {
				out.ReachableSources++
			}
		}
	}
	out.ReachableSources += len(sources)
	if len(sources) > opts.MaxPaths {
		sources = sources[:opts.MaxPaths]
	}
	out.PathsTruncated = out.ReachableSources > len(sources)
	for _, source := range sources {
		path, err := s.path(ctx, source, id, opts)
		if err != nil {
			return nil, err
		}
		out.Paths = append(out.Paths, path)
	}
	return out, ctx.Err()
}

// Explain accepts an allocation address or an interior pointer. Classification
// is relative to selectors[0]; all requested selections receive reachability
// results, even if they cannot reach the target. Path omissions are explicit.
func (s *Snapshot) Explain(ctx context.Context, address uint64, selectors []Request, opts ExplainOptions) (*ObjectExplanation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	var err error
	opts, err = opts.normalized()
	if err != nil {
		return nil, err
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("at least one selector is required")
	}
	for _, req := range selectors {
		if e := ValidateRequest(&req, s.opts); e != nil {
			return nil, e
		}
	}
	id, ok := s.analysis.Graph.FindObjectContaining(address)
	if !ok {
		return nil, fmt.Errorf("no heap object contains %s", hexAddress(address))
	}
	in, err := s.inspect(ctx, selectors[0])
	if err != nil {
		return nil, err
	}
	return s.explainObject(ctx, id, selectors, in, opts)
}

// ExplainTop locates the largest reachable, shared, and exclusive objects, and
// explains a bounded round-robin sample from those rankings. top must be positive;
// representativeLimit=0 defaults to 3. All aggregate totals/distributions are exact.
func (s *Snapshot) ExplainTop(ctx context.Context, req Request, top, representativeLimit int, opts ExplainOptions) (*AggregateExplanation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if top <= 0 || representativeLimit < 0 {
		return nil, fmt.Errorf("top must be positive and representative limit non-negative")
	}
	if representativeLimit == 0 {
		representativeLimit = 3
	}
	var err error
	opts, err = opts.normalized()
	if err != nil {
		return nil, err
	}
	objects, in, err := s.objects(ctx, req, ObjectOptions{Limit: top, By: opts.By, GroupLimit: opts.MaxGroups})
	if err != nil {
		return nil, err
	}
	out := &AggregateExplanation{ObjectsResult: *objects, LargestShared: []ObjectInfo{}, LargestExclusive: []ObjectInfo{}, Explanations: []ObjectExplanation{}}
	shared := intersection(in.selected, in.outside)
	exclusive := difference(in.selected, in.outside)
	for _, part := range []struct {
		set       objectSet
		out       *[]ObjectInfo
		truncated *bool
	}{{shared, &out.LargestShared, &out.SharedTruncated}, {exclusive, &out.LargestExclusive, &out.ExclusiveTruncated}} {
		ids, e := s.rankedIDs(ctx, part.set, "size", top)
		if e != nil {
			return nil, e
		}
		*part.truncated = len(ids) < len(part.set)
		for _, id := range ids {
			*part.out = append(*part.out, s.objectInfo(id, in))
		}
		if e = s.memberships(ctx, *part.out, opts.By, opts.MaxGroups); e != nil {
			return nil, e
		}
	}
	candidates := []snapshotgraph.ObjectID{}
	seen := objectSet{}
	maxRows := max(len(out.Objects), len(out.LargestShared), len(out.LargestExclusive))
	for i := 0; i < maxRows; i++ {
		for _, list := range [][]ObjectInfo{out.Objects, out.LargestShared, out.LargestExclusive} {
			if i < len(list) {
				id := list[i].ID
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					candidates = append(candidates, id)
				}
			}
		}
	}
	if len(candidates) > representativeLimit {
		candidates = candidates[:representativeLimit]
		out.RepresentativesTruncated = true
	}
	for _, id := range candidates {
		e, err := s.explainObject(ctx, id, []Request{req}, in, opts)
		if err != nil {
			return nil, err
		}
		out.Explanations = append(out.Explanations, *e)
	}
	return out, ctx.Err()
}
