package bundle

import (
	"bytes"
	"fmt"
	"runtime/pprof"
	"time"
)

const (
	HeapProfileMember            = "profiles/heap.pb.gz"
	GoroutineProfileMember       = "profiles/goroutine.pb.gz"
	DefaultMaxProfileBytes int64 = 64 << 20
)

// ProfileMeta records independent collection intervals. Profiles are not
// synchronized with WriteHeapDump and heap samples are not full reachability.
type ProfileMeta struct {
	Kind       string `json:"kind"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func profileKind(name string) string {
	switch name {
	case HeapProfileMember:
		return "heap"
	case GoroutineProfileMember:
		return "goroutine"
	}
	return ""
}
func collectProfile(kind string) ([]byte, ProfileMeta, error) {
	start := time.Now().UTC().Format(time.RFC3339Nano)
	var buf bytes.Buffer
	p := pprof.Lookup(kind)
	if p == nil {
		return nil, ProfileMeta{}, fmt.Errorf("bundle: profile %q unavailable", kind)
	}
	if err := p.WriteTo(&buf, 0); err != nil {
		return nil, ProfileMeta{}, fmt.Errorf("bundle: capture %s: %w", kind, err)
	}
	return buf.Bytes(), ProfileMeta{kind, start, time.Now().UTC().Format(time.RFC3339Nano)}, nil
}
