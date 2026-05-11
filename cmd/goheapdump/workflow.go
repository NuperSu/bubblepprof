package main

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"

	"delve_first_project/goheap"
)

const maxWarnings = 500

type analysisResult struct {
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
	addr uintptr
	obj  *goheap.LiveObject
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
		goroutines: make([]*goroutineAnalysis, 0, len(gs)),
	}

	for _, g := range gs {
		ga := &goroutineAnalysis{
			id:       g.ID,
			readable: g.Unreadable == nil,
			labels:   g.Labels(),
		}
		if ga.readable {
			ga.live = goheap.New(d, loadCfg)
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

func printAnalysisReport(w io.Writer, r *analysisResult) {
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

	// Bubble hierarchy grouped by pprof label key.
	for _, pk := range r.keys {
		fmt.Fprintf(w, "\n=== pprof key %q (%d bubbles) ===\n", pk.name, len(pk.bubbles))

		for _, pb := range pk.bubbles {
			ids := make([]int64, len(pb.goroutines))
			for i, ga := range pb.goroutines {
				ids[i] = ga.id
			}
			fmt.Fprintf(w, "\n  -- bubble %q=%q (%d goroutines: %s) --\n",
				pb.key, pb.value, len(pb.goroutines), formatIDs(ids))

			objectCount, edgeCount, typeCounts := collectBubbleStats(pb.goroutines)
			fmt.Fprintf(w, "  aggregate: objects=%d edges=%d\n", objectCount, edgeCount)
			printTopTypes(w, typeCounts, "  ")
		}
	}

	// Unlabeled goroutines aggregate.
	if len(r.unlabeled) > 0 {
		fmt.Fprintf(w, "\n=== unlabeled goroutines (%d goroutines) ===\n", len(r.unlabeled))
		objectCount, edgeCount, typeCounts := collectBubbleStats(r.unlabeled)
		fmt.Fprintf(w, "  aggregate: objects=%d edges=%d\n", objectCount, edgeCount)
		printTopTypes(w, typeCounts, "  ")
	}

	// Per-goroutine detail (unchanged from before).
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
				typeName := normalizeTypeName(entry.obj.Var)
				fmt.Fprintf(w, "  %5d. 0x%x %s (children=%d)\n", i+1, entry.addr, typeName, len(entry.obj.Children))
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

	for addr, obj := range live.All() {
		objectCount++
		edgeCount += len(obj.Children)
		objects = append(objects, objectEntry{addr: addr, obj: obj})

		t := normalizeTypeName(obj.Var)
		byType[t]++
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
		return objects[i].addr < objects[j].addr
	})

	return objectCount, edgeCount, typeCounts, objects
}

// collectBubbleStats merges objects from all goroutines in a group,
// deduplicating by address so shared objects are counted once.
func collectBubbleStats(goroutines []*goroutineAnalysis) (int, int, []typeCount) {
	seen := make(map[uintptr]bool)
	objectCount := 0
	edgeCount := 0
	byType := make(map[string]int, 128)

	for _, ga := range goroutines {
		if ga.live == nil {
			continue
		}
		for addr, obj := range ga.live.All() {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			objectCount++
			edgeCount += len(obj.Children)
			t := normalizeTypeName(obj.Var)
			byType[t]++
		}
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

	return objectCount, edgeCount, typeCounts
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

func normalizeTypeName(v *proc.Variable) string {
	if v == nil {
		return "<unknown>"
	}
	if ts := v.TypeString(); ts != "" {
		return ts
	}
	if v.Kind != reflect.Invalid {
		return v.Kind.String()
	}
	return "<unknown>"
}
