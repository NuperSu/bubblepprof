package snapshotgraph

// ReachableFrom walks the graph iteratively starting from each root and
// returns the set of object IDs reachable from any of them. It handles
// cycles, self-edges, and shared objects.
func ReachableFrom(g *Graph, roots []RootRef) map[ObjectID]struct{} {
	reachable := make(map[ObjectID]struct{}, len(roots))
	if g == nil || len(roots) == 0 {
		return reachable
	}

	stack := make([]ObjectID, 0, len(roots))
	for _, r := range roots {
		if _, ok := reachable[r.ObjectID]; ok {
			continue
		}
		reachable[r.ObjectID] = struct{}{}
		stack = append(stack, r.ObjectID)
	}

	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		obj := &g.Objects[id]
		for _, child := range obj.Children {
			if _, ok := reachable[child]; ok {
				continue
			}
			reachable[child] = struct{}{}
			stack = append(stack, child)
		}
	}
	return reachable
}
