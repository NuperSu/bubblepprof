package bundle

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Bundle is an opened capture artifact ready for analysis.
type Bundle struct {
	Meta Meta
	// HeapDumpPath is the heap dump extracted to a temp file (removed
	// by Close).
	HeapDumpPath string
	// Segments serves the saved read-only segments as an
	// addrspace.Reader; nil when the bundle carries no rodata snapshot.
	Segments *SegmentsReader
	// Warnings to append to analysis diagnostics: the bundle's own
	// warnings plus the standard literal-label phrasing when the rodata
	// snapshot is absent or partial.
	Warnings []string
}

// Close removes the extracted heap dump temp file. Safe to call
// multiple times.
func (b *Bundle) Close() error {
	if b == nil || b.HeapDumpPath == "" {
		return nil
	}
	err := os.Remove(b.HeapDumpPath)
	b.HeapDumpPath = ""
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Open reads a bundle tar in a single pass. Member order is not
// significant; unknown members are ignored for forward compatibility.
// Callers must Close the returned Bundle.
func Open(r io.Reader) (*Bundle, error) {
	var (
		meta        *Meta
		infos       []SegmentInfo
		segData     = map[string][]byte{}
		dumpPath    string
		dumpPresent bool
	)
	cleanupDump := func() {
		if dumpPath != "" {
			_ = os.Remove(dumpPath)
		}
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanupDump()
			return nil, fmt.Errorf("bundle: read tar: %w", err)
		}
		switch {
		case hdr.Name == MetaMember:
			var m Meta
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				cleanupDump()
				return nil, fmt.Errorf("bundle: parse %s: %w", MetaMember, err)
			}
			meta = &m
		case hdr.Name == SegmentsMember:
			if err := json.NewDecoder(tr).Decode(&infos); err != nil {
				cleanupDump()
				return nil, fmt.Errorf("bundle: parse %s: %w", SegmentsMember, err)
			}
		case hdr.Name == HeapDumpMember:
			f, err := os.CreateTemp("", "bubblepprof-bundle-*.heap")
			if err != nil {
				return nil, fmt.Errorf("bundle: create heap dump temp file: %w", err)
			}
			dumpPath = f.Name()
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil {
				cleanupDump()
				return nil, fmt.Errorf("bundle: extract %s: %w", HeapDumpMember, errors.Join(copyErr, closeErr))
			}
			dumpPresent = true
		case strings.HasPrefix(hdr.Name, segmentMemberPrefix) && strings.HasSuffix(hdr.Name, segmentMemberSuffix):
			data, err := io.ReadAll(tr)
			if err != nil {
				cleanupDump()
				return nil, fmt.Errorf("bundle: read %s: %w", hdr.Name, err)
			}
			segData[hdr.Name] = data
		default:
			// Unknown member: ignore (forward compatibility).
		}
	}

	if meta == nil {
		cleanupDump()
		return nil, fmt.Errorf("bundle: missing %s member", MetaMember)
	}
	if meta.FormatVersion <= 0 {
		cleanupDump()
		return nil, fmt.Errorf("bundle: missing or invalid format_version")
	}
	if meta.FormatVersion > FormatVersion {
		cleanupDump()
		return nil, fmt.Errorf("bundle: format version %d is newer than this bubblepprof (supports up to %d)", meta.FormatVersion, FormatVersion)
	}
	if !dumpPresent {
		cleanupDump()
		return nil, fmt.Errorf("bundle: missing %s member", HeapDumpMember)
	}

	b := &Bundle{Meta: *meta, HeapDumpPath: dumpPath}
	b.Warnings = append(b.Warnings, meta.Warnings...)

	if len(infos) > 0 {
		data := make([][]byte, len(infos))
		for i, info := range infos {
			d, ok := segData[info.Member]
			if !ok {
				cleanupDump()
				return nil, fmt.Errorf("bundle: %s references missing member %s", SegmentsMember, info.Member)
			}
			data[i] = d
		}
		sr, err := NewSegmentsReader(infos, data)
		if err != nil {
			cleanupDump()
			return nil, err
		}
		b.Segments = sr
	}

	// When the rodata snapshot is absent or partial, surface the same
	// literal-label phrasing the in-process endpoint uses so
	// string_missing diagnostics read identically in both modes.
	switch meta.Rodata.Status {
	case RodataOK:
	case RodataDisabled:
		b.Warnings = append(b.Warnings, "process memory reader disabled by options; literal pprof label strings may be unrecoverable")
	case RodataTruncated:
		b.Warnings = append(b.Warnings, rodataWarning("rodata snapshot truncated", meta.Rodata.Reason))
	default: // RodataUnavailable and anything unrecognized
		b.Warnings = append(b.Warnings, rodataWarning("process memory reader unavailable", meta.Rodata.Reason))
	}
	return b, nil
}

func rodataWarning(prefix, reason string) string {
	if reason != "" {
		prefix += ": " + reason
	}
	return prefix + "; literal pprof label strings may be unrecoverable"
}
