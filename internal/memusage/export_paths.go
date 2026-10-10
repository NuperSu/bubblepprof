package memusage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
	"github.com/google/pprof/profile"
)

const DefaultPprofPathDepth = 64

// PprofOptions selects the representation, not the reachability measurement.
// Mode defaults to summary. MaxPathDepth=0 renders full paths; the CLI defaults
// to DefaultPprofPathDepth. Every reachable object is always represented.
type PprofOptions struct {
	Mode         string
	MaxPathDepth int
}

const pathExportSemantics = "Synthetic reference paths, not execution or allocation stacks. One shortest representative path per distinct reachable object; choosing a goroutine does not establish ownership. Shared objects are counted once, not once per path. Values are shallow bytes/counts; cumulative widths are not dominator retained sizes. Separate selectors are not additive. Omitted-reference markers abbreviate paths without dropping objects or changing totals."

// ExportPprof preserves the summary representation and optionally exports a
// predecessor forest as synthetic stacks. It retains no extra graph or cache.
func (s *Snapshot) ExportPprof(ctx context.Context, req Request, opts PprofOptions) (*profile.Profile, error) {
	if opts.Mode == "" {
		opts.Mode = "summary"
	}
	if opts.Mode != "summary" && opts.Mode != "paths" {
		return nil, fmt.Errorf("pprof mode must be summary or paths")
	}
	if opts.MaxPathDepth < 0 || opts.MaxPathDepth == 1 {
		return nil, fmt.Errorf("max path depth must be 0 or at least 2")
	}
	if opts.Mode == "summary" {
		if opts.MaxPathDepth != 0 {
			return nil, fmt.Errorf("max path depth requires paths mode")
		}
		r, err := s.Export(ctx, req)
		if err != nil {
			return nil, err
		}
		p, err := r.Pprof()
		if err != nil {
			return nil, err
		}
		return p, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	in, err := s.inspect(ctx, req)
	if err != nil {
		return nil, err
	}
	// Every partition is a subset of this total. Guard signed pprof aggregation.
	m, err := s.measure(in.selected)
	if err != nil {
		return nil, err
	}
	if m.Bytes > math.MaxInt64 {
		return nil, fmt.Errorf("reachable byte total exceeds pprof int64 range")
	}
	var goroutines []snapshotgraph.GoroutineReachability
	for _, g := range s.analysis.Goroutines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := in.gids[g.GoroutineID]; ok {
			goroutines = append(goroutines, g)
		}
	}
	sort.SliceStable(goroutines, func(i, j int) bool { return goroutines[i].GoroutineID < goroutines[j].GoroutineID })
	var roots []exportRoot
	for _, g := range goroutines {
		for _, r := range g.Roots {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			roots = append(roots, exportRoot{g.GoroutineID, r})
		}
	}
	forest, err := buildExportForest(ctx, s.analysis.Graph, roots, opts.MaxPathDepth/2)
	if err != nil {
		return nil, err
	}
	p := newPprof(pathExportSemantics)
	if !s.metadata.ReferenceProvenance {
		p.Comments = append(p.Comments, "Reference slot provenance was not retained; unavailable pointer offsets are omitted.")
	}
	frames := pprofFrames{p: p, locations: map[string]*profile.Location{}}
	keys := make([]string, 0, len(req.Labels))
	for k := range req.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var selection []string
	for _, k := range keys {
		selection = append(selection, strconv.Quote(k)+"="+strconv.Quote(req.Labels[k]))
	}
	selectionLoc := frames.location("selection " + strings.Join(selection, " "))
	partitions := [2]*profile.Location{frames.location("exclusive reachability"), frames.location("shared reachability")}
	objects := s.analysis.Graph.Objects
	objectLocations := make([]*profile.Location, len(objects))
	for i, node := range forest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if node.depth == 0 {
			continue
		}
		id := snapshotgraph.ObjectID(i)
		if _, ok := in.selected[id]; !ok {
			return nil, fmt.Errorf("reference forest reached object outside selection: %d", id)
		}
		prefix, suffix, omitted, err := exportPathSegments(ctx, forest, id, opts.MaxPathDepth)
		if err != nil {
			return nil, err
		}
		// pprof stores the leaf first. Build in root order, then reverse.
		root := roots[node.root]
		rootName := "root " + root.ref.Kind
		if root.ref.Detail != "" {
			rootName += " in " + root.ref.Detail
		}
		partition := 0
		if _, shared := in.outside[id]; shared {
			partition = 1
		}
		locations := []*profile.Location{selectionLoc, partitions[partition], frames.location(fmt.Sprintf("representative goroutine %d", root.gid)), frames.location(rootName)}
		appendObjects := func(ids []snapshotgraph.ObjectID) error {
			for i, step := range ids {
				if i%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				if loc := objectLocations[step]; loc != nil {
					locations = append(locations, loc)
					continue
				}
				o := objects[step]
				name := fmt.Sprintf("object 0x%x shallow=%d bytes", o.Addr, o.Size)
				ref := forest[step].reference
				if forest[step].depth > 1 && ref.OffsetKnown {
					name += fmt.Sprintf(" via pointer +0x%x", ref.Offset)
				}
				loc := frames.location(name)
				objectLocations[step] = loc
				locations = append(locations, loc)
			}
			return nil
		}
		if err := appendObjects(prefix); err != nil {
			return nil, err
		}
		if omitted > 0 {
			locations = append(locations, frames.location(fmt.Sprintf("... %d object reference steps omitted ...", omitted)))
		}
		if err := appendObjects(suffix); err != nil {
			return nil, err
		}
		for l, r := 0, len(locations)-1; l < r; l, r = l+1, r-1 {
			locations[l], locations[r] = locations[r], locations[l]
		}
		values := []int64{1, int64(objects[i].Size), 0, 0, 0, 0}
		values[2+partition*2], values[3+partition*2] = 1, int64(objects[i].Size)
		labels := map[string][]string{}
		for _, k := range keys {
			labels[k] = []string{req.Labels[k]}
		}
		// Append rather than overwrite a user selector using this namespace.
		labels["bubblepprof.object_address"] = append(labels["bubblepprof.object_address"], hexAddress(objects[i].Addr))
		labels["bubblepprof.object_id"] = append(labels["bubblepprof.object_id"], strconv.FormatUint(uint64(id), 10))
		p.Sample = append(p.Sample, &profile.Sample{Location: locations, Value: values, Label: labels})
	}
	if len(p.Sample) != len(in.selected) {
		return nil, fmt.Errorf("reference forest does not cover selected reachable objects")
	}
	if err := p.CheckValid(); err != nil {
		return nil, err
	}
	return p, ctx.Err()
}

type pprofFrames struct {
	p         *profile.Profile
	locations map[string]*profile.Location
}

func (f *pprofFrames) location(name string) *profile.Location {
	if loc := f.locations[name]; loc != nil {
		return loc
	}
	id := uint64(len(f.p.Function)) + 1
	fn := &profile.Function{ID: id, Name: "[bubblepprof reference] " + name, SystemName: "[bubblepprof reference] " + name}
	loc := &profile.Location{ID: id, Line: []profile.Line{{Function: fn}}}
	f.p.Function = append(f.p.Function, fn)
	f.p.Location = append(f.p.Location, loc)
	f.locations[name] = loc
	return loc
}

type exportRoot struct {
	gid uint64
	ref snapshotgraph.RootRef
}

type exportPredecessor struct {
	parent, prefixEnd snapshotgraph.ObjectID
	root, depth       int // depth=0 means unvisited; roots have depth=1
	reference         snapshotgraph.Reference
}

// One BFS covers all objects. Prefix anchors let bounded rendering skip a long
// omitted middle in O(1), rather than repeatedly walking it for every sample.
func buildExportForest(ctx context.Context, g *snapshotgraph.Graph, roots []exportRoot, prefixDepth int) ([]exportPredecessor, error) {
	forest := make([]exportPredecessor, len(g.Objects))
	queue := make([]snapshotgraph.ObjectID, 0)
	valid := func(id snapshotgraph.ObjectID) bool { return uint64(id) < uint64(len(forest)) }
	for i, r := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := r.ref.ObjectID
		if !valid(id) || forest[id].depth != 0 {
			continue
		}
		forest[id] = exportPredecessor{root: i, depth: 1, prefixEnd: id}
		queue = append(queue, id)
	}
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := queue[head]
		node := forest[id]
		// Index the first slot per target once, avoiding a slot scan per child.
		refs := map[snapshotgraph.ObjectID]snapshotgraph.Reference{}
		if uint64(id) < uint64(len(g.References)) {
			for i, r := range g.References[id] {
				if i%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				if _, ok := refs[r.Target]; !ok {
					refs[r.Target] = r
				}
			}
		}
		for i, child := range g.Objects[id].Children {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if !valid(child) || forest[child].depth != 0 {
				continue
			}
			prefixEnd := node.prefixEnd
			if node.depth < prefixDepth {
				prefixEnd = child
			}
			forest[child] = exportPredecessor{parent: id, prefixEnd: prefixEnd, root: node.root, depth: node.depth + 1, reference: refs[child]}
			queue = append(queue, child)
		}
	}
	return forest, ctx.Err()
}

func exportPathSegments(ctx context.Context, forest []exportPredecessor, id snapshotgraph.ObjectID, maxDepth int) (prefix, suffix []snapshotgraph.ObjectID, omitted int, err error) {
	depth := forest[id].depth
	collect := func(id snapshotgraph.ObjectID, n int) ([]snapshotgraph.ObjectID, error) {
		out := make([]snapshotgraph.ObjectID, n)
		for i := n - 1; i >= 0; i-- {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			out[i] = id
			id = forest[id].parent
		}
		return out, ctx.Err()
	}
	if maxDepth == 0 || depth <= maxDepth {
		prefix, err = collect(id, depth)
		return prefix, nil, 0, err
	}
	first := maxDepth / 2
	prefix, err = collect(forest[id].prefixEnd, first)
	if err != nil {
		return nil, nil, 0, err
	}
	suffix, err = collect(id, maxDepth-first)
	return prefix, suffix, depth - maxDepth, err
}
