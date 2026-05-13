// Package bubblereport builds offline bubble heap reports from a
// snapshotgraph.Analysis and a resolved goroutine ID -> labels mapping.
//
// A "bubble" is a single (label key, label value) tuple. Goroutines with
// multiple label keys (for example bubble=alpha + tenant=acme) belong to
// multiple LabelGroups, one per key, so the report can answer "heap
// usage per bubble" along each dimension independently. Exclusive and
// shared accounting is computed strictly within one label key — mixing
// keys would conflate different grouping dimensions.
package bubblereport

import "bubblepprof/internal/snapshotgraph"

// Options tunes Build.
type Options struct {
	// IncludeSystem includes runtime/system goroutines (g0, GC workers,
	// the finalizer goroutine, etc.) in the bubble report. Off by
	// default — these goroutines hold references to runtime metadata
	// that would inflate every bubble.
	IncludeSystem bool

	// IncludeUnlabeled creates a synthetic "unlabeled" bubble per
	// label key containing the user goroutines that the resolver
	// could not label. Off by default; diagnostics still report them.
	IncludeUnlabeled bool

	// LabelKeyFilter limits the report to a single label key (for
	// example "bubble"). Empty means "all keys".
	LabelKeyFilter string

	// UnlabeledKey is the label key under which unlabeled goroutines
	// are reported when IncludeUnlabeled is set. Defaults to "bubble".
	UnlabeledKey string

	// UnlabeledValue is the value used for the synthetic unlabeled
	// bubble. Defaults to "<unlabeled>".
	UnlabeledValue string
}

// Input describes what Build needs.
type Input struct {
	Analysis    *snapshotgraph.Analysis
	LabelsByGID map[uint64]map[string]string
	Options     Options
}

// Report is the result of Build. Groups are sorted by label key, and
// within each group bubbles are sorted by label value.
type Report struct {
	Groups      []LabelGroup
	Diagnostics Diagnostics
}

// LabelGroup is the set of bubbles for one label key.
type LabelGroup struct {
	Key     string
	Bubbles []Bubble

	// SharedObjects is the union of objects reachable from more than
	// one bubble inside this group. SharedBytes is the corresponding
	// byte total.
	SharedObjects map[snapshotgraph.ObjectID]struct{}
	SharedBytes   uint64
}

// Bubble is one (key, value) grouping.
type Bubble struct {
	Key   string
	Value string

	GoroutineIDs []uint64

	ReachableObjects map[snapshotgraph.ObjectID]struct{}
	ReachableBytes   uint64

	ExclusiveObjects map[snapshotgraph.ObjectID]struct{}
	ExclusiveBytes   uint64

	SharedObjects map[snapshotgraph.ObjectID]struct{}
	SharedBytes   uint64

	GlobalOverlapObjects int
	GlobalOverlapBytes   uint64

	SystemOverlapObjects int
	SystemOverlapBytes   uint64
}

// Diagnostics summarises label coverage.
type Diagnostics struct {
	HeapGoroutines          int
	UserGoroutines          int
	SystemGoroutines        int
	LabeledUserGoroutines   int
	UnlabeledUserGoroutines int
	IgnoredSystemGoroutines int
}
