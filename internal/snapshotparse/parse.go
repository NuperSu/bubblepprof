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
