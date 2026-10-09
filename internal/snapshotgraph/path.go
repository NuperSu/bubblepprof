package snapshotgraph

import "context"

// Witness is a shortest object-edge path from the supplied roots. RootIndex
// identifies the originating root. SearchLimited is never evidence of absence.
type Witness struct {
	Found         bool
	SearchLimited bool
	RootIndex     int
	Objects       []ObjectID
	References    []Reference // parallel to transitions Objects[i] -> Objects[i+1]
}

// FindPath performs iterative multi-root BFS. maxVisited=0 permits at most V
// visited objects. Invalid references are ignored consistently with ReachableFrom.
// Children/root order breaks ties deterministically. Missing slot provenance is
// explicit; it does not prevent finding a graph path.
func FindPath(ctx context.Context, g *Graph, roots []RootRef, target ObjectID, maxVisited int) (Witness, error) {
	out := Witness{RootIndex: -1, Objects: []ObjectID{}, References: []Reference{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !g.validID(target) {
		return out, nil
	}
	type predecessor struct {
		parent    ObjectID
		root      int
		hasParent bool
	}
	seen := map[ObjectID]predecessor{}
	queue := []ObjectID{}
	limited := false
	add := func(id ObjectID, p predecessor) {
		if !g.validID(id) {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		if maxVisited > 0 && len(seen) >= maxVisited {
			limited = true
			return
		}
		seen[id] = p
		queue = append(queue, id)
	}
	for i, r := range roots {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		add(r.ObjectID, predecessor{root: i})
	}
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		id := queue[head]
		if id == target {
			out.Found = true
			out.RootIndex = seen[id].root
			for {
				out.Objects = append(out.Objects, id)
				p := seen[id]
				if !p.hasParent {
					break
				}
				id = p.parent
			}
			for i, j := 0, len(out.Objects)-1; i < j; i, j = i+1, j-1 {
				out.Objects[i], out.Objects[j] = out.Objects[j], out.Objects[i]
			}
			for i := 0; i+1 < len(out.Objects); i++ {
				from, to := out.Objects[i], out.Objects[i+1]
				ref := Reference{Target: to}
				if uint64(from) < uint64(len(g.References)) {
					for _, r := range g.References[from] {
						if r.Target == to {
							ref = r
							break
						}
					}
				}
				out.References = append(out.References, ref)
			}
			return out, nil
		}
		for i, child := range g.Objects[id].Children {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return out, err
				}
			}
			add(child, predecessor{parent: id, root: seen[id].root, hasParent: true})
		}
	}
	out.SearchLimited = limited
	return out, ctx.Err()
}
