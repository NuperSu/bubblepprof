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
	live *goheap.LiveObjects

	goroutinesTotal int
	goroutinesRead  int
	framesTotal     int
	localsTotal     int

	warnings []string
}

type typeCount struct {
	typeName string
	count    int
}

func buildHeapGraph(d *debugger.Debugger, o options) (*analysisResult, error) {
	gs, err := listAllGoroutines(d, o.pageSize)
	if err != nil {
		return nil, fmt.Errorf("list goroutines: %w", err)
	}

	// MaxVariableRecurse is kept at 1 so that a single LocalVariables call
	// never explodes into an exponential tree (cyclic/fan-out structures).
	// goheap lazily re-evaluates pointers it discovers, with deduplication,
	// so the full graph is still traversed.
	loadCfg := proc.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 1,
		MaxStringLen:       o.maxStringLen,
		MaxArrayValues:     o.maxArrayValues,
		MaxStructFields:    o.maxStructFields,
	}

	res := &analysisResult{
		live:            goheap.New(d, loadCfg),
		goroutinesTotal: len(gs),
	}

	for _, g := range gs {
		if g.Unreadable != nil {
			res.addWarning("goroutine %d unreadable: %v", g.ID, g.Unreadable)
			continue
		}
		res.goroutinesRead++

		const maxDepth = 8192
		frames, err := d.Stacktrace(g.ID, maxDepth, api.StacktraceOptions(0))
		if err != nil {
			res.addWarning("goroutine %d stacktrace error: %v", g.ID, err)
			continue
		}
		if len(frames) == maxDepth {
			res.addWarning("goroutine %d stacktrace truncated at %d frames", g.ID, maxDepth)
		}

		for i, fr := range frames {
			res.framesTotal++
			if fr.Err != nil {
				res.addWarning("goroutine %d frame %d error: %v", g.ID, i, fr.Err)
			}

			locals, err := d.LocalVariables(g.ID, i, 0, loadCfg)
			if err != nil {
				res.addWarning("goroutine %d frame %d locals error: %v", g.ID, i, err)
				continue
			}

			for _, v := range locals {
				res.localsTotal++
				res.live.Add(v, g.ID, i)
			}
		}
	}

	return res, nil
}

func (r *analysisResult) addWarning(format string, args ...any) {
	if len(r.warnings) >= maxWarnings {
		return
	}
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func printAnalysisReport(w io.Writer, r *analysisResult) {
	objectCount := 0
	edgeCount := 0
	byType := make(map[string]int, 128)

	for _, obj := range r.live.All() {
		objectCount++
		edgeCount += len(obj.Children)

		t := "<unknown>"
		if obj.Var != nil {
			t = obj.Var.TypeString()
			if t == "" {
				t = obj.Var.Kind.String()
			}
		}
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

	fmt.Fprintf(w, "goroutines: total=%d readable=%d\n", r.goroutinesTotal, r.goroutinesRead)
	fmt.Fprintf(w, "frames scanned: %d\n", r.framesTotal)
	fmt.Fprintf(w, "root locals traversed: %d\n", r.localsTotal)
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

	if len(r.warnings) > 0 {
		fmt.Fprintln(w, "warnings:")
		for _, warning := range r.warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
		if len(r.warnings) == maxWarnings {
			fmt.Fprintf(w, "  - warning limit reached (%d)\n", maxWarnings)
		}
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
