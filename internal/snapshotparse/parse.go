// Package snapshotparse glues the snapshot tar bundle format and the
// heap dump parser. It exposes a streaming helper that parses heap.dump
// directly out of the tar reader without first loading it fully into
// memory.
package snapshotparse

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"

	"bubblepprof/internal/bubblelabels"
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/heapsnapshot"
	"bubblepprof/internal/snapshot"
)

// Result holds the outputs of parsing one snapshot.tar.
type Result struct {
	Snapshot             *heapsnapshot.HeapSnapshot
	Metadata             snapshot.SnapshotMetadata
	HeapDumpSize         int64
	GoroutineProfileSize int64
}

// BubbleResult is the Phase 5 bundle: a parsed heap dump plus the sidecar
// files needed to recover pprof labels and build bubble reports. heap.dump
// is still streamed and never held fully in memory; the small sidecars
// (goroutine.pprof, labels.json, goroutine.stacks) are loaded into byte
// slices for downstream parsers.
type BubbleResult struct {
	Snapshot         *heapsnapshot.HeapSnapshot
	Metadata         snapshot.SnapshotMetadata
	HeapDumpSize     int64
	GoroutineProfile []byte
	GoroutineStacks  []byte
	Labels           *bubblelabels.Manifest

	// LabelsRaw retains the labels.json bytes verbatim. Useful for
	// callers that want to surface the manifest as JSON without
	// re-marshaling.
	LabelsRaw []byte
}

// ParseSnapshot reads a snapshot tar bundle from r and parses its embedded
// heap.dump into a HeapSnapshot. It also reads metadata.json and records
// the size of goroutine.pprof but does not parse the goroutine profile.
//
// The function streams: it never materializes heap.dump fully in memory.
func ParseSnapshot(r io.Reader, opts heapdump.Options) (*Result, error) {
	tr := tar.NewReader(r)
	res := &Result{}

	var haveHeapDump, haveMetadata, haveProfile bool

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		switch hdr.Name {
		case snapshot.HeapDumpFile:
			snap, err := heapdump.Parse(tr, opts)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", snapshot.HeapDumpFile, err)
			}
			res.Snapshot = snap
			res.HeapDumpSize = hdr.Size
			haveHeapDump = true
		case snapshot.MetadataFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", snapshot.MetadataFile, err)
			}
			if err := json.Unmarshal(b, &res.Metadata); err != nil {
				return nil, fmt.Errorf("decode %s: %w", snapshot.MetadataFile, err)
			}
			haveMetadata = true
		case snapshot.GoroutineProfileFile:
			res.GoroutineProfileSize = hdr.Size
			haveProfile = true
		}
	}

	if !haveHeapDump {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.HeapDumpFile)
	}
	if !haveProfile {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.GoroutineProfileFile)
	}
	if !haveMetadata {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.MetadataFile)
	}
	if res.Metadata.Format != snapshot.FormatV1 {
		return nil, fmt.Errorf("unsupported snapshot format %q", res.Metadata.Format)
	}
	return res, nil
}

// BubbleParseOptions tunes ParseSnapshotForBubbles.
type BubbleParseOptions struct {
	HeapDump heapdump.Options

	// RequireProfile makes a missing goroutine.pprof an error. By
	// default, callers that have labels.json may proceed without it.
	RequireProfile bool
	// RequireLabels makes a missing labels.json an error. Most callers
	// leave it false because labels.json is optional.
	RequireLabels bool
}

// ParseSnapshotForBubbles reads a snapshot tar bundle and returns the
// information required to build bubble reports: a parsed HeapSnapshot
// (streamed) plus the small sidecar files (goroutine.pprof bytes,
// optional labels.json, optional goroutine.stacks).
//
// heap.dump is never loaded fully into memory. labels.json and
// goroutine.stacks are loaded into memory; both are optional.
func ParseSnapshotForBubbles(r io.Reader, opts BubbleParseOptions) (*BubbleResult, error) {
	tr := tar.NewReader(r)
	res := &BubbleResult{}

	var haveHeapDump, haveMetadata, haveProfile bool

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		switch hdr.Name {
		case snapshot.HeapDumpFile:
			snap, err := heapdump.Parse(tr, opts.HeapDump)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", snapshot.HeapDumpFile, err)
			}
			res.Snapshot = snap
			res.HeapDumpSize = hdr.Size
			haveHeapDump = true
		case snapshot.MetadataFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", snapshot.MetadataFile, err)
			}
			if err := json.Unmarshal(b, &res.Metadata); err != nil {
				return nil, fmt.Errorf("decode %s: %w", snapshot.MetadataFile, err)
			}
			haveMetadata = true
		case snapshot.GoroutineProfileFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", snapshot.GoroutineProfileFile, err)
			}
			res.GoroutineProfile = b
			haveProfile = true
		case snapshot.LabelsFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", snapshot.LabelsFile, err)
			}
			m, err := bubblelabels.DecodeManifest(b)
			if err != nil {
				return nil, fmt.Errorf("decode %s: %w", snapshot.LabelsFile, err)
			}
			res.Labels = m
			res.LabelsRaw = b
		case snapshot.GoroutineStacksFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", snapshot.GoroutineStacksFile, err)
			}
			res.GoroutineStacks = b
		}
	}

	if !haveHeapDump {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.HeapDumpFile)
	}
	if !haveMetadata {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.MetadataFile)
	}
	if opts.RequireProfile && !haveProfile {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.GoroutineProfileFile)
	}
	if opts.RequireLabels && res.Labels == nil {
		return nil, fmt.Errorf("snapshot missing %s", snapshot.LabelsFile)
	}
	if res.Metadata.Format != snapshot.FormatV1 {
		return nil, fmt.Errorf("unsupported snapshot format %q", res.Metadata.Format)
	}
	return res, nil
}
