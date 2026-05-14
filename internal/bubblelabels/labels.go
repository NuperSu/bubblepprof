// Package bubblelabels owns the persisted shape of the goroutine -> pprof
// label mapping that bubblepprof instrumentation records at runtime, plus
// a concurrency-safe in-process Registry that produces it.
//
// The manifest is what ships in snapshot.tar as labels.json. The runtime
// public API in pkg/bubblepprof writes into a Registry; the capture path
// snapshots the Registry into a Manifest at snapshot time.
package bubblelabels

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Format identifiers and tar file names used by Phase 5.
const (
	ManifestFormatV1 = "bubblepprof-labels-v1"
)

// LabelSet is an ordered list of key/value labels. Order is preserved so
// runtime/pprof tooling can present labels deterministically; the Map
// helper produces the equivalent unordered map.
type LabelSet struct {
	pairs []labelPair
}

type labelPair struct {
	Key, Value string
}

// MakeLabelSet returns a LabelSet built from alternating key/value strings.
// It returns an error if kv has an odd length. Duplicate keys keep the
// last value, matching runtime/pprof.Labels semantics.
func MakeLabelSet(kv ...string) (LabelSet, error) {
	if len(kv)%2 != 0 {
		return LabelSet{}, fmt.Errorf("bubblepprof: expected even number of key/value strings, got %d", len(kv))
	}
	ls := LabelSet{pairs: make([]labelPair, 0, len(kv)/2)}
	seen := make(map[string]int, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, v := kv[i], kv[i+1]
		if idx, ok := seen[k]; ok {
			ls.pairs[idx].Value = v
			continue
		}
		seen[k] = len(ls.pairs)
		ls.pairs = append(ls.pairs, labelPair{Key: k, Value: v})
	}
	return ls, nil
}

// Len reports how many distinct keys the set holds.
func (l LabelSet) Len() int { return len(l.pairs) }

// Map returns a plain map view of the set. The returned map is a copy.
func (l LabelSet) Map() map[string]string {
	if len(l.pairs) == 0 {
		return nil
	}
	m := make(map[string]string, len(l.pairs))
	for _, p := range l.pairs {
		m[p.Key] = p.Value
	}
	return m
}

// Range calls f for each key/value pair in insertion order. It mirrors
// runtime/pprof.LabelSet.Range so callers can adapt one to the other.
func (l LabelSet) Range(f func(key, value string) bool) {
	for _, p := range l.pairs {
		if !f(p.Key, p.Value) {
			return
		}
	}
}

// Equal reports whether two label sets carry the same key/value pairs,
// regardless of order.
func (l LabelSet) Equal(other LabelSet) bool {
	if len(l.pairs) != len(other.pairs) {
		return false
	}
	if len(l.pairs) == 0 {
		return true
	}
	om := other.Map()
	for _, p := range l.pairs {
		if v, ok := om[p.Key]; !ok || v != p.Value {
			return false
		}
	}
	return true
}

// GoroutineLabels is one goroutine -> labels entry in the persisted
// Manifest. Labels are stored as a plain map to keep the on-disk format
// independent of pkg-internal ordering.
type GoroutineLabels struct {
	ID     uint64            `json:"id"`
	Labels map[string]string `json:"labels"`
	Source string            `json:"source,omitempty"`
}

// Manifest is the on-disk shape of labels.json.
type Manifest struct {
	Format     string            `json:"format"`
	CreatedAt  time.Time         `json:"created_at"`
	Goroutines []GoroutineLabels `json:"goroutines"`
}

// Marshal renders m as canonical labels.json bytes.
func (m Manifest) Marshal() ([]byte, error) {
	if m.Format == "" {
		m.Format = ManifestFormatV1
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal labels manifest: %w", err)
	}
	return append(b, '\n'), nil
}

// ReadManifest decodes a Manifest from r. It returns an error if the
// format is not recognized.
func ReadManifest(r io.Reader) (*Manifest, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read labels manifest: %w", err)
	}
	return DecodeManifest(b)
}

// DecodeManifest decodes a Manifest from its serialized labels.json bytes.
func DecodeManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decode labels manifest: %w", err)
	}
	if m.Format != ManifestFormatV1 {
		return nil, fmt.Errorf("unsupported labels manifest format %q", m.Format)
	}
	return &m, nil
}

// Registry holds the live goroutine -> label mapping. It is safe to use
// from multiple goroutines. The zero value is ready to use.
//
// Nested registrations are supported via a per-goroutine stack: when Push
// returns, the caller invokes the returned closer to restore the previous
// label set on that goroutine.
type Registry struct {
	mu     sync.RWMutex
	stacks map[uint64][]map[string]string
}

// NewRegistry returns a fresh empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Push attaches labels to goroutine id and returns a closer that pops the
// pushed entry, restoring the previous (if any) label set. Closer is safe
// to call multiple times.
func (r *Registry) Push(id uint64, labels map[string]string) func() {
	if r == nil || id == 0 {
		return func() {}
	}
	cp := copyLabels(labels)
	r.mu.Lock()
	if r.stacks == nil {
		r.stacks = make(map[uint64][]map[string]string)
	}
	r.stacks[id] = append(r.stacks[id], cp)
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { r.popOnce(id) })
	}
}

func (r *Registry) popOnce(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stacks == nil {
		return
	}
	stack := r.stacks[id]
	if len(stack) == 0 {
		return
	}
	stack = stack[:len(stack)-1]
	if len(stack) == 0 {
		delete(r.stacks, id)
	} else {
		r.stacks[id] = stack
	}
}

// Set replaces any existing stack entry for goroutine id with a single
// frame containing labels. Use this for callers that do not need nested
// scoping (for example, when adopting an already-running goroutine).
func (r *Registry) Set(id uint64, labels map[string]string) {
	if r == nil || id == 0 {
		return
	}
	cp := copyLabels(labels)
	r.mu.Lock()
	if r.stacks == nil {
		r.stacks = make(map[uint64][]map[string]string)
	}
	r.stacks[id] = []map[string]string{cp}
	r.mu.Unlock()
}

// Clear removes any label state recorded for goroutine id.
func (r *Registry) Clear(id uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.stacks, id)
	r.mu.Unlock()
}

// Lookup returns the topmost label map for goroutine id, or nil if the
// goroutine has no recorded labels. The returned map is a copy.
func (r *Registry) Lookup(id uint64) map[string]string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stack := r.stacks[id]
	if len(stack) == 0 {
		return nil
	}
	return copyLabels(stack[len(stack)-1])
}

// Len reports the number of goroutines that currently have labels.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.stacks)
}

// Snapshot returns a Manifest containing one entry per registered
// goroutine with the topmost (current) label set. Snapshot is a copy and
// is safe to mutate.
//
// source, if non-empty, is recorded on every entry. Production code passes
// "bubblepprof.Do" or similar; tests can pass anything.
func (r *Registry) Snapshot(now time.Time, source string) Manifest {
	m := Manifest{
		Format:    ManifestFormatV1,
		CreatedAt: now.UTC(),
	}
	if r == nil {
		return m
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	m.Goroutines = make([]GoroutineLabels, 0, len(r.stacks))
	for id, stack := range r.stacks {
		if len(stack) == 0 {
			continue
		}
		m.Goroutines = append(m.Goroutines, GoroutineLabels{
			ID:     id,
			Labels: copyLabels(stack[len(stack)-1]),
			Source: source,
		})
	}
	// Sort for deterministic output. Avoid pulling in sort just for this:
	// the goroutine count is bounded and ordering only matters for
	// determinism in tests and human-readable diffs.
	sortByID(m.Goroutines)
	return m
}

func copyLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortByID(xs []GoroutineLabels) {
	// Insertion sort is fine: snapshots are small (process goroutine
	// counts measured in thousands, with the labeled subset typically
	// orders of magnitude smaller) and we avoid adding a sort import to
	// keep this file dependency-light.
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j].ID < xs[j-1].ID; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
