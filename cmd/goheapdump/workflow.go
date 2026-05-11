package main

import (
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/debugger"

	"delve_first_project/goheap"
)

const maxWarnings = 500

type analysisResult struct {
	// Current grouping is one graph per goroutine. The thesis target is one
	// graph per pprof bubble, so this is still an intermediate shape.
	goroutines []*goroutineAnalysis
}

type goroutineAnalysis struct {
	id int64

	readable bool

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
	for _, g := range r.goroutines {
		if g.readable {
			readable++
		}
	}

	fmt.Fprintf(w, "goroutines: total=%d readable=%d\n", len(r.goroutines), readable)

	for _, g := range r.goroutines {
		fmt.Fprintf(w, "\n== goroutine %d ==\n", g.id)
		if !g.readable {
			fmt.Fprintln(w, "state: unreadable")
			printWarnings(w, g.warnings)
			continue
		}

		objectCount, edgeCount, typeCounts, objects := collectGraphStats(g.live)

		fmt.Fprintf(w, "frames scanned: %d\n", g.framesTotal)
		fmt.Fprintf(w, "root locals traversed: %d\n", g.localsTotal)
		fmt.Fprintf(w, "graph: objects=%d edges=%d\n", objectCount, edgeCount)

		if len(typeCounts) > 0 {
			fmt.Fprintln(w, "top object types:")
			limit := 10
			if len(typeCounts) < limit {
				limit = len(typeCounts)
			}
			for i := 0; i < limit; i++ {
				fmt.Fprintf(w, "  %2d. %s (%d)\n", i+1, typeCounts[i].typeName, typeCounts[i].count)
			}
		}

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
