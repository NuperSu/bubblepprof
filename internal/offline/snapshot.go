// Package offline loads reusable snapshot sessions from capture bundles.
package offline

import (
	"context"
	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"os"
)

type Snapshot struct {
	*memusage.Snapshot
	Capture bundle.Meta
}

func OpenSnapshot(ctx context.Context, path string, opts memusage.Options) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := bundle.Open(f)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	return LoadBundle(ctx, b, opts)
}

func LoadBundle(ctx context.Context, b *bundle.Bundle, opts memusage.Options) (*Snapshot, error) {
	f, err := os.Open(b.HeapDumpPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var extra addrspace.Reader
	if b.Segments != nil {
		extra = b.Segments
	}
	s, err := memusage.LoadSnapshot(ctx, f, f, extra, b.Warnings, opts)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Snapshot: s, Capture: b.Meta}, nil
}
