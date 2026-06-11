package bundle

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Segment is one read-only memory segment to embed in a bundle. R must
// yield exactly Size bytes.
type Segment struct {
	Addr  uint64
	Size  uint64
	Perms string
	Path  string
	R     io.Reader
}

// WriteInput is the content of one bundle. The writer fills
// Meta.FormatVersion and the rodata segment/byte counts; everything
// else (including Rodata.Status) is the caller's responsibility.
type WriteInput struct {
	Meta         Meta
	Segments     []Segment
	HeapDump     io.Reader
	HeapDumpSize int64
}

// Write streams a format-version-1 bundle tar to w: meta.json, then
// rodata/segments.json and rodata/NNNNN.bin members, then heap.dump.
func Write(w io.Writer, in WriteInput) error {
	meta := in.Meta
	meta.FormatVersion = FormatVersion
	if meta.CreatedAt == "" {
		meta.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if meta.Warnings == nil {
		meta.Warnings = []string{}
	}
	meta.Rodata.Segments = len(in.Segments)
	meta.Rodata.TotalBytes = 0
	for _, s := range in.Segments {
		meta.Rodata.TotalBytes += s.Size
	}

	tw := tar.NewWriter(w)

	metaBytes, err := json.MarshalIndent(&meta, "", "  ")
	if err != nil {
		return fmt.Errorf("bundle: marshal meta.json: %w", err)
	}
	if err := writeMember(tw, MetaMember, metaBytes); err != nil {
		return err
	}

	if len(in.Segments) > 0 {
		infos := make([]SegmentInfo, len(in.Segments))
		for i, s := range in.Segments {
			infos[i] = SegmentInfo{
				Member: segmentMemberName(i),
				Addr:   HexUint64(s.Addr),
				Size:   s.Size,
				Perms:  s.Perms,
				Path:   s.Path,
			}
		}
		segBytes, err := json.MarshalIndent(infos, "", "  ")
		if err != nil {
			return fmt.Errorf("bundle: marshal %s: %w", SegmentsMember, err)
		}
		if err := writeMember(tw, SegmentsMember, segBytes); err != nil {
			return err
		}
		for i, s := range in.Segments {
			name := segmentMemberName(i)
			if err := writeHeader(tw, name, int64(s.Size)); err != nil {
				return err
			}
			if _, err := io.CopyN(tw, s.R, int64(s.Size)); err != nil {
				return fmt.Errorf("bundle: write %s: %w", name, err)
			}
		}
	}

	if err := writeHeader(tw, HeapDumpMember, in.HeapDumpSize); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, in.HeapDump, in.HeapDumpSize); err != nil {
		return fmt.Errorf("bundle: write %s: %w", HeapDumpMember, err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("bundle: finalize tar: %w", err)
	}
	return nil
}

func writeHeader(tw *tar.Writer, name string, size int64) error {
	err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o600,
		Size: size,
	})
	if err != nil {
		return fmt.Errorf("bundle: write %s header: %w", name, err)
	}
	return nil
}

func writeMember(tw *tar.Writer, name string, contents []byte) error {
	if err := writeHeader(tw, name, int64(len(contents))); err != nil {
		return err
	}
	if _, err := tw.Write(contents); err != nil {
		return fmt.Errorf("bundle: write %s: %w", name, err)
	}
	return nil
}
