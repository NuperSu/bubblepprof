//go:build darwin

package addrspace

import (
	"debug/gosym"
	"debug/macho"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
)

const (
	machoVMProtRead  uint32 = 0x01
	machoVMProtWrite uint32 = 0x02
)

type machoSegment struct {
	addr   uint64 // on-disk vmaddr (pre-ASLR)
	filesz uint64
	offset uint64 // file offset
}

// ProcessReader reads bytes from the current process's virtual address
// space using ASLR-corrected Mach-O segment offsets. Only read-only
// file-backed segments are eligible; writable segments (heap, stack,
// anonymous data) are excluded to match the semantics of the Linux
// /proc/self/mem implementation.
type ProcessReader struct {
	file     *os.File
	path     string
	segments []machoSegment
	slide    int64 // runtimeAddr - onDiskVmaddr
}

// machSlideProbe is a do-nothing function whose runtime address (via
// reflect) minus its on-disk pclntab address gives the current ASLR
// slide. It must not be inlined so FuncForPC can find it.
//
//go:noinline
func machSlideProbe() {}

// OpenSelfProcessReader opens the current executable, parses its
// readable Mach-O segments, computes the ASLR slide, and returns a
// reader. Callers must Close it.
func OpenSelfProcessReader() (*ProcessReader, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("addrspace: executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: resolve symlinks for %q: %w", exe, err)
	}

	mf, err := macho.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: parse Mach-O %q: %w", exe, err)
	}
	segs, slide, parseErr := parseMachOSegments(mf)
	_ = mf.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("addrspace: %w", parseErr)
	}

	f, err := os.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: open %q for reading: %w", exe, err)
	}
	return &ProcessReader{file: f, path: exe, segments: segs, slide: slide}, nil
}

func parseMachOSegments(f *macho.File) ([]machoSegment, int64, error) {
	slide, err := computeMachOSlide(f)
	if err != nil {
		return nil, 0, err
	}
	var segs []machoSegment
	for _, l := range f.Loads {
		s, ok := l.(*macho.Segment)
		if !ok {
			continue
		}
		if s.Prot&machoVMProtRead == 0 {
			continue
		}
		if s.Prot&machoVMProtWrite != 0 {
			continue
		}
		if s.Filesz == 0 {
			continue
		}
		segs = append(segs, machoSegment{
			addr:   s.Addr,
			filesz: s.Filesz,
			offset: s.Offset,
		})
	}
	return segs, slide, nil
}

// computeMachOSlide determines the ASLR slide by comparing the
// runtime PC of machSlideProbe against its on-disk address from the
// binary's pclntab. Both values use the same function name as the
// common key, so the computation is robust regardless of the module
// import path.
func computeMachOSlide(f *macho.File) (int64, error) {
	text := f.Section("__text")
	if text == nil {
		return 0, fmt.Errorf("binary has no __text section")
	}
	pclntabSect := f.Section("__gopclntab")
	if pclntabSect == nil {
		return 0, fmt.Errorf("binary has no __gopclntab section (binary may be stripped)")
	}
	pclntabData, err := pclntabSect.Data()
	if err != nil {
		return 0, fmt.Errorf("read __gopclntab: %w", err)
	}

	lineTable := gosym.NewLineTable(pclntabData, text.Addr)
	symTable, err := gosym.NewTable(nil, lineTable)
	if err != nil {
		return 0, fmt.Errorf("parse pclntab: %w", err)
	}

	rpc := reflect.ValueOf(machSlideProbe).Pointer()
	rtFn := runtime.FuncForPC(rpc)
	if rtFn == nil {
		return 0, fmt.Errorf("runtime.FuncForPC returned nil for probe")
	}
	sym := symTable.LookupFunc(rtFn.Name())
	if sym == nil {
		return 0, fmt.Errorf("probe %q not found in pclntab", rtFn.Name())
	}
	return int64(rpc) - int64(sym.Entry), nil
}

// Close releases the underlying executable file.
func (r *ProcessReader) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Name implements NamedReader.
func (r *ProcessReader) Name() string { return "process" }

// Mappings returns nil on Darwin; segment eligibility is enforced
// during OpenSelfProcessReader, not per-read.
func (r *ProcessReader) Mappings() []Mapping {
	if r == nil {
		return nil
	}
	return nil
}

// ReadAtAddr implements Reader. It corrects addr for the ASLR slide,
// looks up the result in the read-only segment table, and reads the
// bytes from the executable file.
func (r *ProcessReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if r == nil || r.file == nil {
		return nil, false
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end, ok := AddUint64(addr, size)
	if !ok {
		return nil, false
	}

	// Convert runtime addresses to on-disk vmaddrs by subtracting slide.
	ondisk := uint64(int64(addr) - r.slide)
	ondiskEnd := uint64(int64(end) - r.slide)

	for _, s := range r.segments {
		segEnd, ok := AddUint64(s.addr, s.filesz)
		if !ok {
			continue
		}
		if ondisk >= s.addr && ondiskEnd <= segEnd {
			fileOff := s.offset + (ondisk - s.addr)
			if fileOff > math.MaxInt64 {
				return nil, false
			}
			buf := make([]byte, size)
			if _, err := r.file.ReadAt(buf, int64(fileOff)); err != nil {
				return nil, false
			}
			return buf, true
		}
	}
	return nil, false
}
