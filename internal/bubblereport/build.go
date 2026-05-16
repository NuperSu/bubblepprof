package bubblereport

import (
	"fmt"
	"sort"

	"bubblepprof/internal/snapshotgraph"
)

// Build produces a Report from the provided input.
//
// The caller is responsible for filling per-goroutine and global
// reachability before calling Build — i.e. running
// snapshotgraph.ComputeReachability on the Analysis (or otherwise
// populating Reachable sets in unit tests). Build itself is a pure
// transform from Analysis to Report.
func Build(in Input) (*Report, error) {
	if in.Analysis == nil {
		return nil, fmt.Errorf("bubblereport: analysis is nil")
	}
	a := in.Analysis
	g := a.Graph
	if g == nil {
		return nil, fmt.Errorf("bubblereport: analysis has no graph")
	}

	opts := in.Options
	if opts.UnlabeledKey == "" {
		opts.UnlabeledKey = "bubble"
	}
	if opts.UnlabeledValue == "" {
		opts.UnlabeledValue = "<unlabeled>"
	}

	// Index per-goroutine reachability by ID for O(1) lookup.
	reachByID := make(map[uint64]map[snapshotgraph.ObjectID]struct{}, len(a.Goroutines))
	isSystem := make(map[uint64]bool, len(a.Goroutines))
	for _, gr := range a.Goroutines {
		reachByID[gr.GoroutineID] = gr.Reachable
		isSystem[gr.GoroutineID] = gr.IsSystem || gr.IsBackground
	}

	// Build the union of system goroutine reachability once for overlap
	// accounting. Note: this includes both system and background.
	systemUnion := make(map[snapshotgraph.ObjectID]struct{})
	for _, gr := range a.Goroutines {
		if !(gr.IsSystem || gr.IsBackground) {
			continue
		}
		for id := range gr.Reachable {
			systemUnion[id] = struct{}{}
		}
	}

	report := &Report{}
	report.Diagnostics.HeapGoroutines = len(a.Goroutines)

	// Aggregate goroutines per (key, value).
	type bubbleKey struct{ key, value string }
	bubbles := make(map[bubbleKey]*Bubble)
	keys := make(map[string]struct{})

	for _, gr := range a.Goroutines {
		if isSystem[gr.GoroutineID] {
			report.Diagnostics.SystemGoroutines++
		} else {
			report.Diagnostics.UserGoroutines++
		}

		labels := in.LabelsByGID[gr.GoroutineID]
		if isSystem[gr.GoroutineID] && !opts.IncludeSystem {
			if len(labels) > 0 {
				// Counted as ignored even if it had labels in the
				// manifest — system goroutines should not contaminate
				// user bubbles.
				report.Diagnostics.IgnoredSystemGoroutines++
			}
			continue
		}

		if len(labels) == 0 {
			if !isSystem[gr.GoroutineID] {
				report.Diagnostics.UnlabeledUserGoroutines++
			}
			if !opts.IncludeUnlabeled {
				continue
			}
			// Place under the configured unlabeled key/value.
			if opts.LabelKeyFilter != "" && opts.LabelKeyFilter != opts.UnlabeledKey {
				continue
			}
			bk := bubbleKey{key: opts.UnlabeledKey, value: opts.UnlabeledValue}
			keys[bk.key] = struct{}{}
			b := bubbles[bk]
			if b == nil {
				b = &Bubble{
					Key:              bk.key,
					Value:            bk.value,
					ReachableObjects: make(map[snapshotgraph.ObjectID]struct{}),
				}
				bubbles[bk] = b
			}
			b.GoroutineIDs = append(b.GoroutineIDs, gr.GoroutineID)
			for id := range reachByID[gr.GoroutineID] {
				b.ReachableObjects[id] = struct{}{}
			}
			continue
		}

		report.Diagnostics.LabeledUserGoroutines++
		for k, v := range labels {
			if opts.LabelKeyFilter != "" && k != opts.LabelKeyFilter {
				continue
			}
			bk := bubbleKey{key: k, value: v}
			keys[k] = struct{}{}
			b := bubbles[bk]
			if b == nil {
				b = &Bubble{
					Key:              k,
					Value:            v,
					ReachableObjects: make(map[snapshotgraph.ObjectID]struct{}),
				}
				bubbles[bk] = b
			}
			b.GoroutineIDs = append(b.GoroutineIDs, gr.GoroutineID)
			for id := range reachByID[gr.GoroutineID] {
				b.ReachableObjects[id] = struct{}{}
			}
		}
	}

	// Snap reachable bytes for each bubble now so exclusive/shared math
	// later can rely on stable counts.
	for _, b := range bubbles {
		b.ReachableBytes = bytesOf(g, b.ReachableObjects)
		sort.Slice(b.GoroutineIDs, func(i, j int) bool {
			return b.GoroutineIDs[i] < b.GoroutineIDs[j]
		})
	}

	// Group bubbles by label key, then compute per-group ownership for
	// shared/exclusive accounting.
	keyList := make([]string, 0, len(keys))
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)

	for _, k := range keyList {
		group := LabelGroup{
			Key:           k,
			SharedObjects: make(map[snapshotgraph.ObjectID]struct{}),
		}
		// owners[obj] = number of bubbles in this group that reach obj
		owners := make(map[snapshotgraph.ObjectID]int)
		groupBubbles := make([]*Bubble, 0)
		for bk, b := range bubbles {
			if bk.key != k {
				continue
			}
			for id := range b.ReachableObjects {
				owners[id]++
			}
			groupBubbles = append(groupBubbles, b)
		}
		sort.Slice(groupBubbles, func(i, j int) bool {
			return groupBubbles[i].Value < groupBubbles[j].Value
		})
		for _, b := range groupBubbles {
			b.ExclusiveObjects = make(map[snapshotgraph.ObjectID]struct{})
			b.SharedObjects = make(map[snapshotgraph.ObjectID]struct{})
			for id := range b.ReachableObjects {
				if owners[id] == 1 {
					b.ExclusiveObjects[id] = struct{}{}
				} else {
					b.SharedObjects[id] = struct{}{}
					group.SharedObjects[id] = struct{}{}
				}
			}
			b.ExclusiveBytes = bytesOf(g, b.ExclusiveObjects)
			b.SharedBytes = bytesOf(g, b.SharedObjects)
			b.GlobalOverlapObjects, b.GlobalOverlapBytes = overlap(g, b.ReachableObjects, a.Globals.Reachable)
			b.SystemOverlapObjects, b.SystemOverlapBytes = overlap(g, b.ReachableObjects, systemUnion)
			group.Bubbles = append(group.Bubbles, *b)
		}
		group.SharedBytes = bytesOf(g, group.SharedObjects)
		report.Groups = append(report.Groups, group)
	}

	return report, nil
}

// bytesOf sums the size of the objects in set.
func bytesOf(g *snapshotgraph.Graph, set map[snapshotgraph.ObjectID]struct{}) uint64 {
	var n uint64
	for id := range set {
		if int(id) < len(g.Objects) {
			n += g.Objects[id].Size
		}
	}
	return n
}

func overlap(g *snapshotgraph.Graph, a, b map[snapshotgraph.ObjectID]struct{}) (int, uint64) {
	var (
		count int
		bytes uint64
	)
	// Iterate over the smaller set.
	if len(b) < len(a) {
		a, b = b, a
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			continue
		}
		count++
		if int(id) < len(g.Objects) {
			bytes += g.Objects[id].Size
		}
	}
	return count, bytes
}
