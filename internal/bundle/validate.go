package bundle

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/NuperSu/bubblepprof/internal/heapdump"
)

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, e := r.r.Read(p)
	r.n += int64(n)
	return n, e
}

// ValidateArchive checks a complete transport artifact without extracting or
// retaining payloads. It validates tar headers/trailers, required members,
// metadata, the heap signature, and segment lengths/indexes. Heap record parsing
// and runtime support checks remain the analyzer's responsibility.
func ValidateArchive(src io.Reader) error {
	r := &countingReader{r: src}
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	sizes := map[string]uint64{}
	var meta Meta
	var infos []SegmentInfo
	var total uint64
	for {
		start := r.n
		h, err := tr.Next()
		if err == io.EOF {
			if r.n-start < 1024 {
				return fmt.Errorf("bundle: missing complete tar end marker")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("bundle: invalid tar: %w", err)
		}
		segment := strings.HasPrefix(h.Name, segmentMemberPrefix) && strings.HasSuffix(h.Name, segmentMemberSuffix)
		known := h.Name == MetaMember || h.Name == HeapDumpMember || h.Name == SegmentsMember || segment || profileKind(h.Name) != ""
		if known {
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
				return fmt.Errorf("bundle: %s is not a regular member", h.Name)
			}
			if seen[h.Name] {
				return fmt.Errorf("bundle: duplicate %s member", h.Name)
			}
			seen[h.Name] = true
		}
		switch {
		case h.Name == MetaMember || h.Name == SegmentsMember:
			limit := int64(DefaultMaxMetadataBytes)
			if h.Name == SegmentsMember {
				limit = DefaultMaxSegmentIndexBytes
			}
			if h.Size > limit {
				return fmt.Errorf("bundle: %s metadata size exceeds limit", h.Name)
			}
			dec := json.NewDecoder(tr)
			if h.Name == MetaMember {
				err = dec.Decode(&meta)
			} else {
				err = dec.Decode(&infos)
			}
			if err != nil {
				return fmt.Errorf("bundle: invalid %s: %w", h.Name, err)
			}
			var trailing any
			if err := dec.Decode(&trailing); err != io.EOF {
				return fmt.Errorf("bundle: trailing JSON in %s", h.Name)
			}
		case h.Name == HeapDumpMember:
			header := make([]byte, len(heapdump.Header)+1)
			if _, err := io.ReadFull(tr, header); err != nil {
				return fmt.Errorf("bundle: truncated heap header: %w", err)
			}
			if !bytes.Equal(header, []byte(heapdump.Header+"\n")) {
				return fmt.Errorf("bundle: unsupported heap dump signature")
			}
		case segment:
			size := uint64(h.Size)
			if total > math.MaxUint64-size {
				return fmt.Errorf("bundle: segment size total overflows")
			}
			total += size
			sizes[h.Name] = size
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return fmt.Errorf("bundle: truncated %s: %w", h.Name, err)
		}
	}
	if !seen[MetaMember] || !seen[HeapDumpMember] {
		return fmt.Errorf("bundle: missing metadata or heap dump")
	}
	if meta.FormatVersion < 1 || meta.FormatVersion > FormatVersion {
		return fmt.Errorf("bundle: unsupported format version %d", meta.FormatVersion)
	}
	if len(sizes) != len(infos) || meta.Rodata.Segments != len(sizes) || meta.Rodata.TotalBytes != total {
		return fmt.Errorf("bundle: segment metadata mismatch")
	}
	indexed := map[string]bool{}
	for _, info := range infos {
		size, ok := sizes[info.Member]
		if !ok || indexed[info.Member] || size != info.Size {
			return fmt.Errorf("bundle: invalid segment index member %q", info.Member)
		}
		if uint64(info.Addr) > math.MaxUint64-info.Size {
			return fmt.Errorf("bundle: segment address overflows")
		}
		indexed[info.Member] = true
	}
	// Consume the rest of the response so transport errors after the tar trailer
	// are not silently accepted. Trailing bytes must be zero tar padding.
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return fmt.Errorf("bundle: unexpected data after tar end marker")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func decodeSingleJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}
