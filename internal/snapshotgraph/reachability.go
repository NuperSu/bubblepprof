package snapshotgraph

// ReachableFrom walks the graph iteratively starting from each root and
// returns the set of object IDs reachable from any of them. It handles
// cycles, self-edges, and shared objects. Invalid object IDs in roots
// or in Children edges are skipped rather than panicking.
func ReachableFrom(g *Graph, roots []RootRef) map[ObjectID]struct{} {
	reachable := make(map[ObjectID]struct{}, len(roots))
	if g == nil || len(roots) == 0 {
		return reachable
	}

	queue := make([]ObjectID, 0, len(roots))
	for _, r := range roots {
		if !g.validID(r.ObjectID) {
			continue
		}
		if _, ok := reachable[r.ObjectID]; ok {
			continue
		}
		reachable[r.ObjectID] = struct{}{}
		queue = append(queue, r.ObjectID)
	}

	for head := 0; head < len(queue); head++ {
		obj := &g.Objects[queue[head]]
		for _, child := range obj.Children {
			if !g.validID(child) {
				continue
			}
			if _, ok := reachable[child]; ok {
				continue
			}
			reachable[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	return reachable
}
