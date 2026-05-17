//go:build windows

package addrspace

import (
	"debug/gosym"
	"debug/pe"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
)

const (
	peImageSCNMemRead  uint32 = 0x40000000
	peImageSCNMemWrite uint32 = 0x80000000
)

type peSection struct {
	virtualAddr uint64 // preferred base + section RVA (on-disk virtual address)
	size        uint64 // raw data size on disk
	offset      uint64 // file offset
}

// ProcessReader reads bytes from the current process's virtual address
// space using ASLR-corrected PE section offsets. Only read-only
// file-backed sections are eligible; writable sections (heap, stack,
// data) are excluded to match the semantics of the Linux
// /proc/self/mem implementation.
type ProcessReader struct {
	file     *os.File
	path     string
	sections []peSection
	slide    int64 // runtimeAddr - onDiskVirtualAddr
}

// peSlideProbe is a do-nothing function whose runtime address (via
// reflect) minus its on-disk pclntab address gives the current ASLR
// slide. It must not be inlined so FuncForPC can find it.
//
//go:noinline
func peSlideProbe() {}

// OpenSelfProcessReader opens the current executable, parses its
// readable PE sections, computes the ASLR slide, and returns a
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

	pf, err := pe.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: parse PE %q: %w", exe, err)
	}
	sects, slide, parseErr := parsePESections(pf)
	pf.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("addrspace: %w", parseErr)
	}

	f, err := os.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: open %q for reading: %w", exe, err)
	}
	return &ProcessReader{file: f, path: exe, sections: sects, slide: slide}, nil
}

func parsePESections(f *pe.File) ([]peSection, int64, error) {
	imageBase, err := peImageBase(f)
	if err != nil {
		return nil, 0, err
	}
	slide, err := computePESlide(f, imageBase)
	if err != nil {
		return nil, 0, err
	}
	var sects []peSection
	for _, s := range f.Sections {
		if s.Characteristics&peImageSCNMemRead == 0 {
			continue
		}
		if s.Characteristics&peImageSCNMemWrite != 0 {
			continue
		}
		if s.Size == 0 {
			continue
		}
		sects = append(sects, peSection{
			virtualAddr: imageBase + uint64(s.VirtualAddress),
			size:        uint64(s.Size),
			offset:      uint64(s.Offset),
		})
	}
	return sects, slide, nil
}

func peImageBase(f *pe.File) (uint64, error) {
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		return uint64(oh.ImageBase), nil
	case *pe.OptionalHeader64:
		return oh.ImageBase, nil
	default:
		return 0, fmt.Errorf("unknown PE optional header type")
	}
}

// computePESlide determines the ASLR slide by comparing the runtime
// PC of peSlideProbe against its on-disk address from the binary's
// pclntab. Both values use the same function name as the common key.
func computePESlide(f *pe.File, imageBase uint64) (int64, error) {
	var textAddr uint64
	var pclntabData []byte
	for _, s := range f.Sections {
		switch s.Name {
		case ".text":
			textAddr = imageBase + uint64(s.VirtualAddress)
		case ".gopclntab":
			data, err := s.Data()
			if err != nil {
				return 0, fmt.Errorf("read .gopclntab: %w", err)
			}
			pclntabData = data
		}
	}
	if pclntabData == nil {
		return 0, fmt.Errorf("binary has no .gopclntab section (binary may be stripped)")
	}
	if textAddr == 0 {
		return 0, fmt.Errorf("binary has no .text section")
	}

	lineTable := gosym.NewLineTable(pclntabData, textAddr)
	symTable, err := gosym.NewTable(nil, lineTable)
	if err != nil {
		return 0, fmt.Errorf("parse pclntab: %w", err)
	}

	rpc := reflect.ValueOf(peSlideProbe).Pointer()
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

// Mappings returns nil on Windows; section eligibility is enforced
// during OpenSelfProcessReader, not per-read.
func (r *ProcessReader) Mappings() []Mapping {
	if r == nil {
		return nil
	}
	return nil
}

// ReadAtAddr implements Reader. It corrects addr for the ASLR slide,
// looks up the result in the read-only section table, and reads the
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

	// Convert runtime addresses to on-disk virtual addresses by subtracting slide.
	ondisk := uint64(int64(addr) - r.slide)
	ondiskEnd := uint64(int64(end) - r.slide)

	for _, s := range r.sections {
		sectEnd, ok := AddUint64(s.virtualAddr, s.size)
		if !ok {
			continue
		}
		if ondisk >= s.virtualAddr && ondiskEnd <= sectEnd {
			fileOff := s.offset + (ondisk - s.virtualAddr)
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
