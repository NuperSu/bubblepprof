package memusage

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

type ObjectOptions struct {
	Sort       string // size (default) or address
	Limit      int    // 0 returns all objects
	By         string // optional grouping label for relationship discovery
	GroupLimit int    // 0 defaults to 20 values per object
}

type ObjectInfo struct {
	SharedObject
	Classification  string   `json:"classification"` // exclusive/shared relative to selection
	Global          bool     `json:"global"`
	OtherSystem     bool     `json:"other_system"`
	OtherGoroutines bool     `json:"other_goroutines"`
	Groups          []string `json:"groups"`
	GroupCount      int      `json:"group_count"`
	GroupsTruncated bool     `json:"groups_truncated"`
}

type SizeBucket struct {
	Min uint64 `json:"min_size"`
	Max uint64 `json:"max_size"`
	Measure
}

type ObjectsResult struct {
	Labels       map[string]string `json:"labels"`
	Goroutines   int               `json:"goroutines"`
	By           string            `json:"by,omitempty"`
	Reachable    Measure           `json:"reachable"`
	Exclusive    Measure           `json:"exclusive"`
	Shared       Measure           `json:"shared"`
	Distribution []SizeBucket      `json:"size_distribution"`
	Objects      []ObjectInfo      `json:"objects"`
	Truncated    bool              `json:"truncated"`
}

type inspection struct {
	selected, global, system, other, outside objectSet
	gids                                     map[uint64]struct{}
}

func (s *Snapshot) inspect(ctx context.Context, req Request) (*inspection, error) {
	roots, gids, err := s.selected(req)
	if err != nil {
		return nil, err
	}
	in := &inspection{gids: gids}
	in.selected, err = s.walk(ctx, roots)
	if err != nil {
		return nil, err
	}
	in.global, err = s.walk(ctx, s.analysis.Globals.Roots)
	if err != nil {
		return nil, err
	}
	var system, other []snapshotgraph.RootRef
	for _, g := range s.analysis.Goroutines {
		if _, ok := gids[g.GoroutineID]; ok {
			continue
		}
		if g.IsSystem || g.IsBackground {
			system = append(system, g.Roots...)
		} else {
			other = append(other, g.Roots...)
		}
	}
	in.system, err = s.walk(ctx, system)
	if err != nil {
		return nil, err
	}
	in.other, err = s.walk(ctx, other)
	if err != nil {
		return nil, err
	}
	in.outside = union(in.global, union(in.system, in.other))
	return in, nil
}

func (s *Snapshot) objectInfo(id snapshotgraph.ObjectID, in *inspection) ObjectInfo {
	o := s.analysis.Graph.Objects[id]
	info := ObjectInfo{SharedObject: SharedObject{id, fmt.Sprintf("0x%x", o.Addr), o.Size, len(o.Children)}, Groups: []string{}}
	_, selected := in.selected[id]
	_, shared := in.outside[id]
	switch {
	case !selected:
		info.Classification = "not_reachable_from_selection"
	case shared:
		info.Classification = "shared"
	default:
		info.Classification = "exclusive"
	}
	_, info.Global = in.global[id]
	_, info.OtherSystem = in.system[id]
	_, info.OtherGoroutines = in.other[id]
	return info
}

func (s *Snapshot) rankedIDs(ctx context.Context, set objectSet, sortBy string, limit int) ([]snapshotgraph.ObjectID, error) {
	ids := make([]snapshotgraph.ObjectID, 0, len(set))
	for id := range set {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	objects := s.analysis.Graph.Objects
	sort.Slice(ids, func(i, j int) bool {
		a, b := objects[ids[i]], objects[ids[j]]
		if sortBy != "address" && a.Size != b.Size {
			return a.Size > b.Size
		}
		if a.Addr != b.Addr {
			return a.Addr < b.Addr
		}
		return ids[i] < ids[j]
	})
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, ctx.Err()
}

func (s *Snapshot) distribution(ctx context.Context, set objectSet) ([]SizeBucket, error) {
	buckets := make([]SizeBucket, 65)
	for id := range set {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size := s.analysis.Graph.Objects[id].Size
		k := bits.Len64(size)
		b := &buckets[k]
		if size > math.MaxUint64-b.Bytes {
			return nil, fmt.Errorf("size distribution byte total overflows uint64")
		}
		b.Objects++
		b.Bytes += size
		if k > 0 {
			b.Min = uint64(1) << uint(k-1)
			b.Max = b.Min - 1 + b.Min
		}
	}
	out := []SizeBucket{}
	for _, b := range buckets {
		if b.Objects > 0 {
			out = append(out, b)
		}
	}
	return out, nil
}

// groupSources is distinct from AND selectors. Only match-eligible goroutines
// with the chosen key participate; missing labels are not an invented group.
func (s *Snapshot) groupSources(by string) []pathSource {
	groups := map[string]*pathSource{}
	for _, g := range s.analysis.Goroutines {
		if !s.opts.IncludeSystemGoroutines && (g.IsSystem || g.IsBackground) {
			continue
		}
		value, ok := s.labels[g.GoroutineID][by]
		if !ok {
			continue
		}
		source := groups[value]
		if source == nil {
			source = &pathSource{category: "group", labels: map[string]string{by: value}, value: value}
			groups[value] = source
		}
		for _, r := range g.Roots {
			source.roots = append(source.roots, r)
			source.gids = append(source.gids, g.GoroutineID)
		}
	}
	values := make([]string, 0, len(groups))
	for v := range groups {
		values = append(values, v)
	}
	sort.Strings(values)
	out := make([]pathSource, 0, len(values))
	for _, v := range values {
		out = append(out, *groups[v])
	}
	return out
}

func (s *Snapshot) memberships(ctx context.Context, objects []ObjectInfo, by string, limit int) error {
	if by == "" {
		return nil
	}
	if limit == 0 {
		limit = 20
	}
	targets := map[snapshotgraph.ObjectID]int{}
	for i, o := range objects {
		targets[o.ID] = i
	}
	if len(targets) == 0 {
		return nil
	}
	for _, group := range s.groupSources(by) {
		set, err := s.walk(ctx, group.roots)
		if err != nil {
			return err
		}
		for id, i := range targets {
			if _, ok := set[id]; ok {
				objects[i].GroupCount++
				if len(objects[i].Groups) < limit {
					objects[i].Groups = append(objects[i].Groups, group.value)
				} else {
					objects[i].GroupsTruncated = true
				}
			}
		}
	}
	return ctx.Err()
}

func (s *Snapshot) objects(ctx context.Context, req Request, opts ObjectOptions) (*ObjectsResult, *inspection, error) {
	if opts.Limit < 0 || opts.GroupLimit < 0 {
		return nil, nil, fmt.Errorf("limits must be non-negative")
	}
	if opts.Sort != "" && opts.Sort != "size" && opts.Sort != "address" {
		return nil, nil, fmt.Errorf("sort must be size or address")
	}
	in, err := s.inspect(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	out := &ObjectsResult{Labels: copyLabels(req.Labels), Goroutines: len(in.gids), By: opts.By, Objects: []ObjectInfo{}}
	for _, p := range []struct {
		dst *Measure
		set objectSet
	}{{&out.Reachable, in.selected}, {&out.Exclusive, difference(in.selected, in.outside)}, {&out.Shared, intersection(in.selected, in.outside)}} {
		*p.dst, err = s.measure(p.set)
		if err != nil {
			return nil, nil, err
		}
	}
	out.Distribution, err = s.distribution(ctx, in.selected)
	if err != nil {
		return nil, nil, err
	}
	ids, err := s.rankedIDs(ctx, in.selected, opts.Sort, opts.Limit)
	if err != nil {
		return nil, nil, err
	}
	out.Truncated = len(ids) < len(in.selected)
	for _, id := range ids {
		out.Objects = append(out.Objects, s.objectInfo(id, in))
	}
	if err = s.memberships(ctx, out.Objects, opts.By, opts.GroupLimit); err != nil {
		return nil, nil, err
	}
	return out, in, ctx.Err()
}

// Objects lists reachable objects and a distribution covering the full selected
// set, regardless of listing limits. Classification compares all modeled roots.
func (s *Snapshot) Objects(ctx context.Context, req Request, opts ObjectOptions) (*ObjectsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	out, _, err := s.objects(ctx, req, opts)
	return out, err
}
