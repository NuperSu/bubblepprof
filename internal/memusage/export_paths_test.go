package memusage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
	"github.com/google/pprof/profile"
)

func profileBytes(t *testing.T, p *profile.Profile) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func profileTotals(p *profile.Profile) []int64 {
	totals := make([]int64, 6)
	for _, sample := range p.Sample {
		for i, v := range sample.Value {
			totals[i] += v
		}
	}
	return totals
}

func sampleNames(sample *profile.Sample) string {
	var names []string
	for _, loc := range sample.Location {
		names = append(names, loc.Line[0].Function.Name)
	}
	return strings.Join(names, "\n")
}

func TestPprofPathsCoverageAndParity(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	ctx := context.Background()
	p, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths", MaxPathDepth: 64})
	if err != nil {
		t.Fatal(err)
	}
	data := profileBytes(t, p)
	p, err = profile.ParseData(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.ExportPprof(ctx, selector("a"), PprofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profileTotals(p), []int64{6, 1112, 3, 1040, 3, 72}) || !reflect.DeepEqual(profileTotals(p), profileTotals(want)) {
		t.Fatal(profileTotals(p), profileTotals(want))
	}
	if len(p.Sample) != 6 || p.DefaultSampleType != "reachable_space" {
		t.Fatal("missing or duplicated objects", len(p.Sample))
	}
	seen := map[int]bool{}
	for _, sample := range p.Sample {
		id, err := strconv.Atoi(sample.Label["bubblepprof.object_id"][0])
		if err != nil || seen[id] {
			t.Fatal("invalid or duplicate sample ID", id, err)
		}
		seen[id] = true
		o := s.analysis.Graph.Objects[id]
		if !reflect.DeepEqual(sample.Label["tenant"], []string{"a"}) || sample.Label["bubblepprof.object_address"][0] != hexAddress(o.Addr) {
			t.Fatal(sample.Label)
		}
		if sample.Value[0] != 1 || sample.Value[1] != int64(o.Size) || sample.Value[2]+sample.Value[4] != 1 || sample.Value[3]+sample.Value[5] != int64(o.Size) {
			t.Fatal(sample.Value)
		}
		names := sampleNames(sample)
		if !strings.Contains(names, "representative goroutine 1") || !strings.Contains(names, "root stack in workerA") || !strings.Contains(names, `selection "tenant"="a"`) {
			t.Fatal(names)
		}
		if id == 2 && (!strings.Contains(names, "object 0x1000") || !strings.Contains(names, "object 0x3000 shallow=64 bytes via pointer +0x8")) {
			t.Fatal("missing interior-pointer reference evidence", names)
		}
		if !strings.Contains(sample.Location[0].Line[0].Function.Name, fmt.Sprintf("object 0x%x shallow=%d bytes", o.Addr, o.Size)) {
			t.Fatal("leaf is not the sampled object", names)
		}
		for _, loc := range sample.Location {
			fn := loc.Line[0].Function
			if loc.Address != 0 || fn.Filename != "" || !strings.HasPrefix(fn.Name, "[bubblepprof reference]") {
				t.Fatal("synthetic reference disguised as code", loc)
			}
		}
	}
	if !reflect.DeepEqual(seen, map[int]bool{0: true, 2: true, 3: true, 4: true, 5: true, 6: true}) {
		t.Fatal(seen)
	}
	again, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths", MaxPathDepth: 64})
	if err != nil || !bytes.Equal(data, profileBytes(t, again)) {
		t.Fatal("non-deterministic export", err)
	}
}

func TestExportForestPathsAndTies(t *testing.T) {
	s := inspectionFixture(t)
	defer s.Close()
	g := s.analysis.Graph
	roots := []exportRoot{{1, s.analysis.Goroutines[0].Roots[0]}, {2, s.analysis.Goroutines[1].Roots[0]}, {1, s.analysis.Goroutines[0].Roots[0]}, {99, snapshotgraph.RootRef{ObjectID: math.MaxUint32}}}
	forest, err := buildExportForest(context.Background(), g, roots, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i, node := range forest {
		if node.depth == 0 {
			continue
		}
		id := snapshotgraph.ObjectID(i)
		path, suffix, omitted, err := exportPathSegments(context.Background(), forest, id, 64)
		if err != nil || len(path) != node.depth || len(suffix) != 0 || omitted != 0 || path[0] != roots[node.root].ref.ObjectID || path[len(path)-1] != id {
			t.Fatal(path, suffix, omitted, node)
		}
		w, err := snapshotgraph.FindPath(context.Background(), g, []snapshotgraph.RootRef{roots[0].ref, roots[1].ref}, id, 0)
		if err != nil || !reflect.DeepEqual(path, w.Objects) {
			t.Fatal("forest differs from BFS witness", path, w, err)
		}
		for j := 1; j < len(path); j++ {
			found := false
			for _, child := range g.Objects[path[j-1]].Children {
				found = found || child == path[j]
			}
			if !found || !reflect.DeepEqual(forest[path[j]].reference, w.References[j-1]) {
				t.Fatal("invalid transition", path[j-1], path[j])
			}
		}
	}
	if forest[2].root != 0 || forest[3].depth != 3 || forest[2].reference.Offset != 8 || forest[2].reference.Pointer != 0x3004 {
		t.Fatal(forest)
	}
	// Reordering the graph's goroutines must not change representative tie breaks.
	req := Request{Labels: map[string]string{"role": "worker"}}
	s.labels[2]["role"] = "worker"
	s.index["role"]["worker"] = []uint64{1, 2}
	p, err := s.ExportPprof(context.Background(), req, PprofOptions{Mode: "paths"})
	if err != nil {
		t.Fatal(err)
	}
	s.analysis.Goroutines[0], s.analysis.Goroutines[1] = s.analysis.Goroutines[1], s.analysis.Goroutines[0]
	q, err := s.ExportPprof(context.Background(), req, PprofOptions{Mode: "paths"})
	if err != nil || !bytes.Equal(profileBytes(t, p), profileBytes(t, q)) {
		t.Fatal("goroutine order changed representative paths", err)
	}
}

func TestPprofPathsLongChainAndTruncation(t *testing.T) {
	const n = 50000
	g := &snapshotgraph.Graph{Objects: make([]snapshotgraph.Object, n)}
	for i := range g.Objects {
		g.Objects[i] = snapshotgraph.Object{ID: snapshotgraph.ObjectID(i), Addr: uint64(i+1) * 16, Size: 8}
		if i+1 < n {
			g.Objects[i].Children = []snapshotgraph.ObjectID{snapshotgraph.ObjectID(i + 1)}
		}
	}
	roots := []exportRoot{{7, snapshotgraph.RootRef{ObjectID: 0, Kind: "stack", Detail: "chainWorker"}}}
	forest, err := buildExportForest(context.Background(), g, roots, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4; i < n; i++ {
		prefix, suffix, omitted, err := exportPathSegments(context.Background(), forest, snapshotgraph.ObjectID(i), 4)
		if err != nil || !reflect.DeepEqual(prefix, []snapshotgraph.ObjectID{0, 1}) || !reflect.DeepEqual(suffix, []snapshotgraph.ObjectID{snapshotgraph.ObjectID(i - 1), snapshotgraph.ObjectID(i)}) || omitted != i-3 {
			t.Fatal(i, prefix, suffix, omitted)
		}
	}
	full, suffix, omitted, err := exportPathSegments(context.Background(), forest, n-1, 0)
	if err != nil || len(full) != n || len(suffix) != 0 || omitted != 0 {
		t.Fatal(len(full), len(suffix), omitted)
	}
	s := newSnapshot(&snapshotgraph.Analysis{Graph: g, Goroutines: []snapshotgraph.GoroutineReachability{{GoroutineID: 7, Roots: []snapshotgraph.RootRef{roots[0].ref}}}}, map[uint64]map[string]string{7: {"role": "chain"}}, Diagnostics{}, Options{})
	defer s.Close()
	p, err := s.ExportPprof(context.Background(), Request{Labels: map[string]string{"role": "chain"}}, PprofOptions{Mode: "paths", MaxPathDepth: 4})
	if err != nil || len(p.Sample) != n || !reflect.DeepEqual(profileTotals(p), []int64{n, n * 8, n, n * 8, 0, 0}) {
		t.Fatal("bounded export dropped objects", err)
	}
	last := p.Sample[n-1]
	if len(last.Location) != 9 || !strings.Contains(sampleNames(last), "49996 object reference steps omitted") || !strings.Contains(last.Location[0].Line[0].Function.Name, "object 0xc3500") {
		t.Fatal(sampleNames(last))
	}
}

func TestPprofPathsErrorsAndMissingProvenance(t *testing.T) {
	s := inspectionFixture(t)
	ctx := context.Background()
	for _, opts := range []PprofOptions{{Mode: "bad"}, {Mode: "paths", MaxPathDepth: -1}, {Mode: "paths", MaxPathDepth: 1}, {Mode: "summary", MaxPathDepth: 2}} {
		if _, err := s.ExportPprof(ctx, selector("a"), opts); err == nil {
			t.Fatal("invalid options accepted", opts)
		}
	}
	empty, err := s.ExportPprof(ctx, selector("absent"), PprofOptions{Mode: "paths"})
	if err != nil || len(empty.Sample) != 0 {
		t.Fatal(empty, err)
	}
	s.analysis.Graph.References = nil
	p, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths"})
	if err != nil || len(p.Sample) != 6 || !strings.Contains(strings.Join(p.Comments, " "), "provenance was not retained") {
		t.Fatal(p, err)
	}
	for _, sample := range p.Sample {
		if strings.Contains(sampleNames(sample), "via pointer") {
			t.Fatal("invented pointer offsets")
		}
	}
	s.diag.FailedGoroutines = 1
	if _, err := s.ExportPprof(ctx, selector("absent"), PprofOptions{Mode: "paths"}); err == nil {
		t.Fatal("label failure reported as successful empty selection")
	}
	s.diag.FailedGoroutines = 0
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ExportPprof(cancelled, selector("a"), PprofOptions{Mode: "paths"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.analysis.Graph.Objects[4].Size = math.MaxInt64 + 1
	if _, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths"}); err == nil || !strings.Contains(err.Error(), "int64") {
		t.Fatal("signed overflow accepted", err)
	}
	s.Close()
	if _, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths"}); err == nil {
		t.Fatal("closed session accepted")
	}
}

type exportCheckContext struct {
	context.Context
	calls, cancelAt int
}

func (c *exportCheckContext) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestPprofPathsCancellationDuringWork(t *testing.T) {
	g := &snapshotgraph.Graph{Objects: make([]snapshotgraph.Object, 5000)}
	for i := 0; i+1 < len(g.Objects); i++ {
		g.Objects[i].Children = []snapshotgraph.ObjectID{snapshotgraph.ObjectID(i + 1)}
	}
	roots := []exportRoot{{1, snapshotgraph.RootRef{ObjectID: 0}}}
	ctx := &exportCheckContext{Context: context.Background(), cancelAt: 20}
	if _, err := buildExportForest(ctx, g, roots, 2); !errors.Is(err, context.Canceled) || ctx.calls != 20 {
		t.Fatal("forest ignored cancellation", err, ctx.calls)
	}
	forest, err := buildExportForest(context.Background(), g, roots, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx = &exportCheckContext{Context: context.Background(), cancelAt: 3}
	if _, _, _, err := exportPathSegments(ctx, forest, snapshotgraph.ObjectID(len(g.Objects)-1), 0); !errors.Is(err, context.Canceled) {
		t.Fatal("unbounded path ignored cancellation", err)
	}
	s := inspectionFixture(t)
	defer s.Close()
	counted := &exportCheckContext{Context: context.Background()}
	if _, err := s.ExportPprof(counted, selector("a"), PprofOptions{Mode: "paths"}); err != nil {
		t.Fatal(err)
	}
	ctx = &exportCheckContext{Context: context.Background(), cancelAt: counted.calls - 3}
	if p, err := s.ExportPprof(ctx, selector("a"), PprofOptions{Mode: "paths"}); !errors.Is(err, context.Canceled) || p != nil {
		t.Fatal("profile assembly ignored cancellation", err)
	}
}

func TestPprofPathsDepthBoundsAndSelectorMetadata(t *testing.T) {
	g := &snapshotgraph.Graph{Objects: make([]snapshotgraph.Object, 8)}
	for i := 0; i+1 < len(g.Objects); i++ {
		g.Objects[i].Children = []snapshotgraph.ObjectID{snapshotgraph.ObjectID(i + 1)}
	}
	for _, limit := range []int{2, 3, 4, 5, 64} {
		forest, err := buildExportForest(context.Background(), g, []exportRoot{{1, snapshotgraph.RootRef{ObjectID: 0}}}, limit/2)
		if err != nil {
			t.Fatal(err)
		}
		prefix, suffix, omitted, err := exportPathSegments(context.Background(), forest, 7, limit)
		if err != nil || prefix[0] != 0 || len(prefix)+len(suffix)+omitted != 8 {
			t.Fatal(limit, prefix, suffix, omitted, err)
		}
		if omitted > 0 && (len(prefix)+len(suffix) != limit || suffix[len(suffix)-1] != 7 || int(suffix[0]-prefix[len(prefix)-1])-1 != omitted) {
			t.Fatal("incorrect path abbreviation", limit, prefix, suffix, omitted)
		}
	}
	s := inspectionFixture(t)
	defer s.Close()
	s.labels[1]["bubblepprof.object_id"] = "user-value"
	p, err := s.ExportPprof(context.Background(), Request{Labels: map[string]string{"tenant": "a", "bubblepprof.object_id": "user-value"}}, PprofOptions{Mode: "paths"})
	if err != nil || len(p.Sample) != 6 {
		t.Fatal(err)
	}
	for _, sample := range p.Sample {
		values := sample.Label["bubblepprof.object_id"]
		if len(values) != 2 || values[0] != "user-value" {
			t.Fatal("selector overwritten by metadata", sample.Label)
		}
	}
}
