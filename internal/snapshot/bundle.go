package snapshot

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type BundleSource struct {
	HeapDump         io.Reader
	HeapDumpSize     int64
	GoroutineProfile []byte
	Metadata         SnapshotMetadata
}

type SnapshotBundle struct {
	HeapDump         []byte
	GoroutineProfile []byte
	Metadata         SnapshotMetadata
}

type SnapshotInfo struct {
	Metadata             SnapshotMetadata
	HeapDumpSize         int64
	GoroutineProfileSize int64
}

func WriteSnapshotBundle(w io.Writer, src BundleSource) error {
	if src.HeapDump == nil {
		return fmt.Errorf("heap dump reader is nil")
	}
	if src.HeapDumpSize < 0 {
		return fmt.Errorf("heap dump size is negative")
	}

	tw := tar.NewWriter(w)

	if err := writeTarEntry(tw, HeapDumpFile, src.HeapDumpSize, src.HeapDump); err != nil {
		return err
	}
	if err := writeTarEntry(tw, GoroutineProfileFile, int64(len(src.GoroutineProfile)), bytes.NewReader(src.GoroutineProfile)); err != nil {
		return err
	}

	metadata, err := json.MarshalIndent(src.Metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metadata = append(metadata, '\n')
	if err := writeTarEntry(tw, MetadataFile, int64(len(metadata)), bytes.NewReader(metadata)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close snapshot tar: %w", err)
	}

	return nil
}

func ReadSnapshotBundle(r io.Reader) (*SnapshotBundle, error) {
	tr := tar.NewReader(r)
	bundle := &SnapshotBundle{}

	var (
		haveHeapDump bool
		haveProfile  bool
		haveMetadata bool
	)

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
		case HeapDumpFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", HeapDumpFile, err)
			}
			bundle.HeapDump = b
			haveHeapDump = true
		case GoroutineProfileFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", GoroutineProfileFile, err)
			}
			bundle.GoroutineProfile = b
			haveProfile = true
		case MetadataFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", MetadataFile, err)
			}
			if err := json.Unmarshal(b, &bundle.Metadata); err != nil {
				return nil, fmt.Errorf("decode %s: %w", MetadataFile, err)
			}
			haveMetadata = true
		}
	}

	if !haveHeapDump {
		return nil, fmt.Errorf("snapshot missing %s", HeapDumpFile)
	}
	if !haveProfile {
		return nil, fmt.Errorf("snapshot missing %s", GoroutineProfileFile)
	}
	if !haveMetadata {
		return nil, fmt.Errorf("snapshot missing %s", MetadataFile)
	}
	if bundle.Metadata.Format != FormatV1 {
		return nil, fmt.Errorf("unsupported snapshot format %q", bundle.Metadata.Format)
	}

	return bundle, nil
}

func InspectSnapshotBundle(r io.Reader) (*SnapshotInfo, error) {
	tr := tar.NewReader(r)
	info := &SnapshotInfo{}

	var (
		haveHeapDump bool
		haveProfile  bool
		haveMetadata bool
	)

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
		case HeapDumpFile:
			info.HeapDumpSize = hdr.Size
			haveHeapDump = true
		case GoroutineProfileFile:
			info.GoroutineProfileSize = hdr.Size
			haveProfile = true
		case MetadataFile:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", MetadataFile, err)
			}
			if err := json.Unmarshal(b, &info.Metadata); err != nil {
				return nil, fmt.Errorf("decode %s: %w", MetadataFile, err)
			}
			haveMetadata = true
		}
	}

	if !haveHeapDump {
		return nil, fmt.Errorf("snapshot missing %s", HeapDumpFile)
	}
	if !haveProfile {
		return nil, fmt.Errorf("snapshot missing %s", GoroutineProfileFile)
	}
	if !haveMetadata {
		return nil, fmt.Errorf("snapshot missing %s", MetadataFile)
	}
	if info.Metadata.Format != FormatV1 {
		return nil, fmt.Errorf("unsupported snapshot format %q", info.Metadata.Format)
	}

	return info, nil
}

func writeTarEntry(tw *tar.Writer, name string, size int64, r io.Reader) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    size,
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := io.Copy(tw, r); err != nil {
		return fmt.Errorf("write tar entry %s: %w", name, err)
	}
	return nil
}
