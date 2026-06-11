package bundle

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
)

// DefaultMaxRodataBytes caps the total read-only segment bytes embedded
// in a bundle when CaptureOptions.MaxRodataBytes is zero.
const DefaultMaxRodataBytes = 256 << 20

// segmentChunkSize bounds individual ReadAtAddr calls while streaming a
// segment into the tar.
const segmentChunkSize = 1 << 20

// CaptureOptions configures CaptureSelf.
type CaptureOptions struct {
	// GCBeforeHeapDump triggers runtime.GC() before the dump so the
	// bundle reflects live memory rather than floating garbage.
	GCBeforeHeapDump bool
	// DisableRodata skips the read-only segment snapshot; literal pprof
	// label strings then surface as string_missing during analysis.
	DisableRodata bool
	// MaxRodataBytes caps total embedded segment bytes; 0 means
	// DefaultMaxRodataBytes. Segments that do not fit are dropped and
	// the rodata status becomes "truncated".
	MaxRodataBytes int64
	// Producer is recorded in meta.json (e.g. "bubblepprof/v0.2.0").
	Producer string
}

// CaptureSelf dumps the calling process's heap, snapshots its eligible
// read-only memory ranges, and streams a format-version-1 bundle tar to
// w. The heap dump is written to a temp file first (debug.WriteHeapDump
// needs a file descriptor and the tar header needs the size up front)
// and removed before returning.
//
// The dump is captured before the rodata ranges are read; the ranges
// are read-only program data, so the two views are consistent without
// stopping the world twice.
func CaptureSelf(ctx context.Context, w io.Writer, opts CaptureOptions) error {
	path, cleanup, err := memusage.RuntimeHeapDumpCapturer{}.CaptureHeapDump(ctx, opts.GCBeforeHeapDump)
	if err != nil {
		return err
	}
	defer cleanup()

	dump, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("bundle: open heap dump: %w", err)
	}
	defer dump.Close()
	st, err := dump.Stat()
	if err != nil {
		return fmt.Errorf("bundle: stat heap dump: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	local := runtimelayout.LocalInput()
	in := WriteInput{
		Meta: Meta{
			Producer:  opts.Producer,
			GoVersion: local.GoVersion,
			GOARCH:    local.GOARCH,
			PtrSize:   local.PtrSize,
			BigEndian: local.BigEndian,
		},
		HeapDump:     dump,
		HeapDumpSize: st.Size(),
	}

	var procReader *addrspace.ProcessReader
	if opts.DisableRodata {
		in.Meta.Rodata = RodataMeta{Status: RodataDisabled, Reason: "disabled by options"}
	} else {
		procReader, err = addrspace.OpenSelfProcessReader()
		if err != nil {
			in.Meta.Rodata = RodataMeta{Status: RodataUnavailable, Reason: err.Error()}
		}
	}
	if procReader != nil {
		defer procReader.Close()
		segments, status := collectSegments(procReader, opts.MaxRodataBytes, &in.Meta)
		in.Segments = segments
		in.Meta.Rodata.Status = status
	}

	return Write(w, in)
}

// collectSegments turns the reader's eligible ranges into bundle
// segments, probing each range's first and last chunk so unreadable
// pseudo-mappings (e.g. [vvar]) are skipped up front instead of
// corrupting the tar mid-stream. Returns the segments and the rodata
// status ("ok" or "truncated"); skipped-unreadable ranges are recorded
// as meta warnings without affecting the status.
func collectSegments(r *addrspace.ProcessReader, maxBytes int64, meta *Meta) ([]Segment, string) {
	budget := maxBytes
	if budget <= 0 {
		budget = DefaultMaxRodataBytes
	}
	status := RodataOK
	var segments []Segment
	for _, m := range r.EligibleStringRanges() {
		size := m.End - m.Start
		if size == 0 {
			continue
		}
		if !probeRange(r, m.Start, size) {
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("skipped unreadable segment 0x%x-0x%x %s", m.Start, m.End, m.Path))
			continue
		}
		if size > uint64(budget) {
			status = RodataTruncated
			meta.Rodata.Reason = "segment snapshot exceeds size cap"
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("dropped segment 0x%x-0x%x %s: rodata size cap exceeded", m.Start, m.End, m.Path))
			continue
		}
		budget -= int64(size)
		segments = append(segments, Segment{
			Addr:  m.Start,
			Size:  size,
			Perms: permString(m),
			Path:  m.Path,
			R:     &rangeReader{r: r, addr: m.Start, remaining: size},
		})
	}
	return segments, status
}

// probeRange checks that the first and last chunk-aligned bytes of the
// range are readable.
func probeRange(r addrspace.Reader, addr, size uint64) bool {
	probe := uint64(segmentChunkSize)
	if probe > size {
		probe = size
	}
	if _, ok := r.ReadAtAddr(addr, probe); !ok {
		return false
	}
	if size > probe {
		if _, ok := r.ReadAtAddr(addr+size-probe, probe); !ok {
			return false
		}
	}
	return true
}

func permString(m addrspace.Mapping) string {
	perms := []byte("---")
	if m.Read {
		perms[0] = 'r'
	}
	if m.Write {
		perms[1] = 'w'
	}
	if m.Exec {
		perms[2] = 'x'
	}
	return string(perms)
}

// rangeReader streams [addr, addr+remaining) through ReadAtAddr in
// bounded chunks. A failed chunk read returns an error, aborting the
// bundle write loudly rather than emitting corrupt segment bytes.
type rangeReader struct {
	r         addrspace.Reader
	addr      uint64
	remaining uint64
}

func (rr *rangeReader) Read(p []byte) (int, error) {
	if rr.remaining == 0 {
		return 0, io.EOF
	}
	n := uint64(len(p))
	if n > rr.remaining {
		n = rr.remaining
	}
	if n > segmentChunkSize {
		n = segmentChunkSize
	}
	buf, ok := rr.r.ReadAtAddr(rr.addr, n)
	if !ok {
		return 0, fmt.Errorf("bundle: segment read failed at 0x%x", rr.addr)
	}
	copy(p, buf)
	rr.addr += n
	rr.remaining -= n
	return int(n), nil
}
