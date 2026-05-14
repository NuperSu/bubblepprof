package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"

	"bubblepprof/goheap"
)

const maxWarnings = 500

type analysisResult struct {
	graph      *goheap.ProcessGraph
	goroutines []*goroutineAnalysis
	keys       []*pprofKey          // pprof label keys, sorted alphabetically
	unlabeled  []*goroutineAnalysis // goroutines with no pprof labels
}

// pprofKey groups all bubbles that share the same label key name.
type pprofKey struct {
	name    string
	bubbles []*pprofBubble
}

// pprofBubble is one key=value pair, containing all goroutines with that label.
type pprofBubble struct {
	key        string
	value      string
	goroutines []*goroutineAnalysis
	reachable  map[goheap.ObjectID]struct{}
}

type goroutineAnalysis struct {
	id int64

	readable bool
	labels   map[string]string // pprof labels from proc.G.Labels()

	live *goheap.LiveObjects

	framesTotal int
	localsTotal int

	warnings []string
}

type typeCount struct {
	typeName string
	count    int
}

type objectEntry struct {
	id  goheap.ObjectID
	obj *goheap.Object
}

func buildHeapGraph(d *debugger.Debugger, o options) (*analysisResult, error) {
	gs, err := listAllGoroutines(d, o.pageSize)
	if err != nil {
		return nil, fmt.Errorf("list goroutines: %w", err)
	}

	// MaxVariableRecurse is kept at 1 so one LocalVariables call does not
	// expand a large or cyclic object graph by itself. goheap then follows
	// discovered typed pointers lazily and deduplicates nodes by address.
	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 1,
		MaxStringLen:       o.maxStringLen,
		MaxArrayValues:     o.maxArrayValues,
		MaxStructFields:    o.maxStructFields,
	}

	res := &analysisResult{
		graph:      goheap.NewProcessGraph(),
		goroutines: make([]*goroutineAnalysis, 0, len(gs)),
	}

	for _, g := range gs {
		ga := &goroutineAnalysis{
			id:       g.ID,
			readable: g.Unreadable == nil,
			labels:   g.Labels(),
		}
		if ga.readable {
			ga.live = goheap.New(res.graph, d, loadCfg, uint64(g.ID), ga.labels)
		} else {
			ga.addWarning("unreadable: %v", g.Unreadable)
			res.goroutines = append(res.goroutines, ga)
			continue
		}

		const maxDepth = 8192
		frames, err := d.Stacktrace(g.ID, maxDepth, api.StacktraceOptions(0))
		if err != nil {
			ga.addWarning("stacktrace error: %v", err)
			res.goroutines = append(res.goroutines, ga)
			continue
		}
		if len(frames) == maxDepth {
			ga.addWarning("stacktrace truncated at %d frames", maxDepth)
		}

		for i, fr := range frames {
			ga.framesTotal++
			if fr.Err != nil {
				ga.addWarning("frame %d error: %v", i, fr.Err)
			}

			locals, err := d.LocalVariables(g.ID, i, 0, loadCfg)
			if err != nil {
				ga.addWarning("frame %d locals error: %v", i, err)
				continue
			}

			for _, v := range locals {
				ga.localsTotal++
				ga.live.Add(v, g.ID, i)
			}
		}

		res.goroutines = append(res.goroutines, ga)
	}

	sort.Slice(res.goroutines, func(i, j int) bool {
		return res.goroutines[i].id < res.goroutines[j].id
	})

	// Build the key → bubble → goroutines index from pprof labels.
	type bubbleKey struct {
		key, value string
	}
	keyMap := make(map[string]*pprofKey)
	bubbleMap := make(map[bubbleKey]*pprofBubble)

	for _, ga := range res.goroutines {
		if len(ga.labels) == 0 {
			res.unlabeled = append(res.unlabeled, ga)
			continue
		}
		for k, v := range ga.labels {
			pk, ok := keyMap[k]
			if !ok {
				pk = &pprofKey{name: k}
				keyMap[k] = pk
			}
			bk := bubbleKey{key: k, value: v}
			pb, ok := bubbleMap[bk]
			if !ok {
				pb = &pprofBubble{key: k, value: v}
				bubbleMap[bk] = pb
				pk.bubbles = append(pk.bubbles, pb)
			}
			pb.goroutines = append(pb.goroutines, ga)
		}
	}

	for _, pb := range bubbleMap {
		pb.reachable = unionReachable(pb.goroutines)
	}

	// Collect and sort keys alphabetically; sort bubbles by value within each key.
	res.keys = make([]*pprofKey, 0, len(keyMap))
	for _, pk := range keyMap {
		sort.Slice(pk.bubbles, func(i, j int) bool {
			return pk.bubbles[i].value < pk.bubbles[j].value
		})
		res.keys = append(res.keys, pk)
	}
	sort.Slice(res.keys, func(i, j int) bool {
		return res.keys[i].name < res.keys[j].name
	})

	return res, nil
}

func (g *goroutineAnalysis) addWarning(format string, args ...any) {
	if len(g.warnings) >= maxWarnings {
		return
	}
	g.warnings = append(g.warnings, fmt.Sprintf(format, args...))
}

func printAnalysisReport(w io.Writer, r *analysisResult, o options) {
	readable := 0
	labeled := 0
	for _, g := range r.goroutines {
		if g.readable {
			readable++
		}
		if len(g.labels) > 0 {
			labeled++
		}
	}

	fmt.Fprintf(w, "goroutines: total=%d readable=%d labeled=%d\n",
		len(r.goroutines), readable, labeled)

	// Aggregate traversal stats across all goroutines.
	var totalStats goheap.TraversalStats
	for _, g := range r.goroutines {
		if g.live == nil {
			continue
		}
		s := g.live.Stats
		totalStats.StackPops += s.StackPops
		totalStats.LoadAtCalls += s.LoadAtCalls
		totalStats.LoadPointeeCalls += s.LoadPointeeCalls
		totalStats.EnsureLoadedCalls += s.EnsureLoadedCalls
		totalStats.PointerReloadCalls += s.PointerReloadCalls
		totalStats.DedupHits += s.DedupHits
		totalStats.WastedLoads += s.WastedLoads
	}
	fmt.Fprintf(w, "traversal stats: stack_pops=%d loadAt_calls=%d (pointee=%d ensure=%d reload=%d) dedup_hits=%d wasted_loads=%d\n",
		totalStats.StackPops, totalStats.LoadAtCalls,
		totalStats.LoadPointeeCalls, totalStats.EnsureLoadedCalls, totalStats.PointerReloadCalls,
		totalStats.DedupHits, totalStats.WastedLoads)
	fmt.Fprintf(w, "process graph: objects=%d\n", len(r.graph.Objects))

	// Bubble hierarchy grouped by pprof label key.
	for _, pk := range r.keys {
		fmt.Fprintf(w, "\n=== pprof key %q (%d bubbles) ===\n", pk.name, len(pk.bubbles))
		fmt.Fprintf(w, "shared heap: objects=%d\n", sharedHeapCount(pk.bubbles))

		for _, pb := range pk.bubbles {
			ids := make([]int64, len(pb.goroutines))
			for i, ga := range pb.goroutines {
				ids[i] = ga.id
			}
			fmt.Fprintf(w, "\n  -- bubble %q=%q (%d goroutines: %s) --\n",
				pb.key, pb.value, len(pb.goroutines), formatIDs(ids))

			exclusiveCount := exclusiveHeapCount(pk.bubbles, pb)
			edgeCount, typeCounts := collectReachableStats(r.graph, pb.reachable)
			fmt.Fprintf(w, "  bubble heap: unique=%d exclusive=%d shared=%d edges=%d\n",
				len(pb.reachable), exclusiveCount, len(pb.reachable)-exclusiveCount, edgeCount)
			printTopTypes(w, typeCounts, "  ")
		}
	}

	// Unlabeled goroutines aggregate.
	if len(r.unlabeled) > 0 {
		fmt.Fprintf(w, "\n=== unlabeled goroutines (%d goroutines) ===\n", len(r.unlabeled))
		reachable := unionReachable(r.unlabeled)
		edgeCount, typeCounts := collectReachableStats(r.graph, reachable)
		objectCount := len(reachable)
		fmt.Fprintf(w, "  aggregate: objects=%d edges=%d\n", objectCount, edgeCount)
		printTopTypes(w, typeCounts, "  ")
	}

	if !o.showGoroutines {
		return
	}

	// Per-goroutine detail is useful for debugging traversal, but the default
	// report is intentionally grouped by pprof bubbles.
	fmt.Fprintln(w, "\n=== per-goroutine detail ===")

	for _, g := range r.goroutines {
		fmt.Fprintf(w, "\n== goroutine %d ==\n", g.id)
		if !g.readable {
			fmt.Fprintln(w, "state: unreadable")
			printWarnings(w, g.warnings)
			continue
		}

		if len(g.labels) > 0 {
			fmt.Fprintf(w, "pprof labels: %s\n", formatLabels(g.labels))
		}

		objectCount, edgeCount, typeCounts, objects := collectGraphStats(g.live)

		fmt.Fprintf(w, "frames scanned: %d\n", g.framesTotal)
		fmt.Fprintf(w, "root locals traversed: %d\n", g.localsTotal)
		fmt.Fprintf(w, "graph: objects=%d edges=%d\n", objectCount, edgeCount)
		printTopTypes(w, typeCounts, "")

		if len(objects) > 0 {
			fmt.Fprintln(w, "objects:")
			for i, entry := range objects {
				fmt.Fprintf(w, "  %5d. 0x%x %s (children=%d)\n", i+1, entry.obj.Addr, entry.obj.TypeName, len(entry.obj.Children))
			}
		}

		printWarnings(w, g.warnings)
	}
}

func collectGraphStats(live *goheap.LiveObjects) (int, int, []typeCount, []objectEntry) {
	if live == nil {
		return 0, 0, nil, nil
	}

	objectCount := 0
	edgeCount := 0
	byType := make(map[string]int, 128)
	objects := make([]objectEntry, 0, 256)

	for id, obj := range live.All() {
		objectCount++
		edgeCount += len(obj.Children)
		objects = append(objects, objectEntry{id: id, obj: obj})

		byType[obj.TypeName]++
	}

	typeCounts := make([]typeCount, 0, len(byType))
	for t, c := range byType {
		typeCounts = append(typeCounts, typeCount{typeName: t, count: c})
	}
	sort.Slice(typeCounts, func(i, j int) bool {
		if typeCounts[i].count == typeCounts[j].count {
			return typeCounts[i].typeName < typeCounts[j].typeName
		}
		return typeCounts[i].count > typeCounts[j].count
	})
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].obj.Addr < objects[j].obj.Addr
	})

	return objectCount, edgeCount, typeCounts, objects
}

func unionReachable(goroutines []*goroutineAnalysis) map[goheap.ObjectID]struct{} {
	reachable := make(map[goheap.ObjectID]struct{})
	for _, ga := range goroutines {
		if ga.live == nil {
			continue
		}
		info := ga.live.Info()
		for id := range info.Reachable {
			reachable[id] = struct{}{}
		}
	}
	return reachable
}

func collectReachableStats(graph *goheap.ProcessGraph, reachable map[goheap.ObjectID]struct{}) (int, []typeCount) {
	edgeCount := 0
	byType := make(map[string]int, 128)

	for id := range reachable {
		obj := graph.Object(id)
		if obj == nil {
			continue
		}
		edgeCount += len(obj.Children)
		byType[obj.TypeName]++
	}

	typeCounts := make([]typeCount, 0, len(byType))
	for t, c := range byType {
		typeCounts = append(typeCounts, typeCount{typeName: t, count: c})
	}
	sort.Slice(typeCounts, func(i, j int) bool {
		if typeCounts[i].count == typeCounts[j].count {
			return typeCounts[i].typeName < typeCounts[j].typeName
		}
		return typeCounts[i].count > typeCounts[j].count
	})

	return edgeCount, typeCounts
}

func exclusiveHeapCount(bubbles []*pprofBubble, target *pprofBubble) int {
	count := 0
	for id := range target.reachable {
		owners := 0
		for _, pb := range bubbles {
			if _, ok := pb.reachable[id]; ok {
				owners++
			}
		}
		if owners == 1 {
			count++
		}
	}
	return count
}

func sharedHeapCount(bubbles []*pprofBubble) int {
	ownersByObject := make(map[goheap.ObjectID]int)
	for _, pb := range bubbles {
		for id := range pb.reachable {
			ownersByObject[id]++
		}
	}

	count := 0
	for _, owners := range ownersByObject {
		if owners > 1 {
			count++
		}
	}
	return count
}

func printTopTypes(w io.Writer, typeCounts []typeCount, indent string) {
	if len(typeCounts) == 0 {
		return
	}
	fmt.Fprintf(w, "%stop object types:\n", indent)
	limit := 10
	if len(typeCounts) < limit {
		limit = len(typeCounts)
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(w, "%s  %2d. %s (%d)\n", indent, i+1, typeCounts[i].typeName, typeCounts[i].count)
	}
}

func formatIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ", ")
}

func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}
	return strings.Join(parts, " ")
}

func printWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "warnings:")
	for _, warning := range warnings {
		fmt.Fprintf(w, "  - %s\n", warning)
	}
	if len(warnings) == maxWarnings {
		fmt.Fprintf(w, "  - warning limit reached (%d)\n", maxWarnings)
	}
}
