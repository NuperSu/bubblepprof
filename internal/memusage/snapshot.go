package memusage

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heaplabels"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/snapshotgraph"
)

// Snapshot owns a single resolved graph. Query and Close may be called concurrently.
// Only the two invariant root sets are cached; selector results are never retained.
type Snapshot struct {
	mu       sync.Mutex
	analysis *snapshotgraph.Analysis
	labels   map[uint64]map[string]string
	index    map[string]map[string][]uint64
	diag     Diagnostics
	opts     Options
	cache    invariantReach
	metadata SnapshotMetadata
}

type SnapshotMetadata struct {
	GoVersion           string                  `json:"go_version"`
	GOARCH              string                  `json:"goarch"`
	Parser              heapsnapshot.ParseStats `json:"parser"`
	Graph               snapshotgraph.Stats     `json:"graph"`
	LabelRecovery       heaplabels.Stats        `json:"label_recovery"`
	RecoveredGoroutines int                     `json:"recovered_goroutines"`
	Warnings            []string                `json:"warnings"`
	Validation          string                  `json:"validation"`
}

// LoadSnapshot constructs a reusable session from dump bytes. Readers are only
// needed during this call; the returned graph retains no lazy file references.
func LoadSnapshot(ctx context.Context, r io.Reader, ra io.ReaderAt, extra addrspace.Reader, warnings []string, opts Options) (*Snapshot, error) {
	return loadSnapshot(ctx, r, ra, DefaultLabelRecoverer{}, extra, warnings, opts)
}

func loadSnapshot(ctx context.Context, r io.Reader, ra io.ReaderAt, recoverer LabelRecoverer, extra addrspace.Reader, warnings []string, opts Options) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap, result, diag, err := parseAndRecoverLabels(ctx, r, ra, recoverer, extra, warnings, opts)
	if err != nil {
		return nil, err
	}
	if diag.UnsupportedRuntime {
		return nil, &UnsupportedRuntimeError{GoVersion: diag.GoVersion, GOARCH: diag.GOARCH}
	}
	for i := range snap.Objects {
		snap.Objects[i].Contents = nil
	}
	a, err := snapshotgraph.Build(snap, snapshotgraph.Options{KeepProvenance: opts.KeepReferenceProvenance})
	if err != nil {
		return nil, fmt.Errorf("build object graph: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := newSnapshot(a, result.LabelsByGID, diag, opts)
	s.metadata.Parser = snap.Stats
	s.metadata.LabelRecovery = result.Stats
	s.metadata.Warnings = append(s.metadata.Warnings, snap.Warnings...)
	if len(s.metadata.Warnings) > 0 {
		s.metadata.Validation = "diagnostics_present"
	}
	return s, nil
}

func newSnapshot(a *snapshotgraph.Analysis, labels map[uint64]map[string]string, diag Diagnostics, opts Options) *Snapshot {
	s := &Snapshot{analysis: a, labels: labels, diag: diag, opts: opts, index: map[string]map[string][]uint64{}}
	for _, g := range a.Goroutines {
		if !opts.IncludeSystemGoroutines && (g.IsSystem || g.IsBackground) {
			continue
		}
		for k, v := range labels[g.GoroutineID] {
			if s.index[k] == nil {
				s.index[k] = map[string][]uint64{}
			}
			s.index[k][v] = append(s.index[k][v], g.GoroutineID)
		}
	}
	for _, values := range s.index {
		for _, gids := range values {
			sort.Slice(gids, func(i, j int) bool { return gids[i] < gids[j] })
		}
	}
	s.metadata = SnapshotMetadata{GoVersion: diag.GoVersion, GOARCH: diag.GOARCH, Graph: a.Stats, RecoveredGoroutines: len(labels), Validation: "parsed", Warnings: append(append([]string{}, diag.Warnings...), a.Warnings...)}
	if diag.FailedGoroutines > 0 || len(s.metadata.Warnings) > 0 || a.Stats.UnresolvedPointers > 0 {
		s.metadata.Validation = "diagnostics_present"
	}
	return s
}

func (s *Snapshot) Query(ctx context.Context, req Request) (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.analysis == nil {
		return nil, fmt.Errorf("snapshot is closed")
	}
	candidates := map[uint64]struct{}{}
	first := true
	for k, v := range req.Labels {
		ids := s.index[k][v]
		next := map[uint64]struct{}{}
		for _, id := range ids {
			if _, ok := candidates[id]; first || ok {
				next[id] = struct{}{}
			}
		}
		candidates = next
		first = false
	}
	if first {
		candidates = nil
	}
	resp, err := computeFromAnalysis(req, s.analysis, s.labels, s.diag, s.opts, &s.cache, candidates)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resp, nil
}

// LabelKeys and LabelValues return sorted copies. GoroutineIDs selects an exact
// key/value group, distinct from the AND selection accepted by Query.
func (s *Snapshot) LabelKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for k := range s.index {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func (s *Snapshot) LabelValues(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for v := range s.index[key] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func (s *Snapshot) GoroutineIDs(key, value string) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64{}, s.index[key][value]...)
}
func (s *Snapshot) Metadata() SnapshotMetadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.metadata
	m.Warnings = append([]string{}, m.Warnings...)
	return m
}
func (s *Snapshot) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.analysis = nil
	s.labels = nil
	s.index = nil
	s.cache = invariantReach{}
	return nil
}
