//go:build windows

package addrspace

import (
	"debug/pe"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

var (
	modkernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = modkernel32.NewProc("GetModuleHandleW")
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
	slide    int64 // actualLoadBase - preferredImageBase
}

// OpenSelfProcessReader opens the current executable, parses its
// readable PE sections, computes the ASLR slide via GetModuleHandle,
// and returns a reader. Callers must Close it.
func OpenSelfProcessReader() (*ProcessReader, error) {
	exe, err := resolveExePath()
	if err != nil {
		return nil, err
	}

	pf, err := pe.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: parse PE %q: %w", exe, err)
	}
	imageBase, sects, parseErr := parsePE(pf)
	pf.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("addrspace: %w", parseErr)
	}

	// GetModuleHandleW(NULL) returns the actual load base of the current
	// executable. Comparing it to the preferred ImageBase from the PE
	// header gives the ASLR slide without needing pclntab.
	r, _, callErr := procGetModuleHandle.Call(0)
	if r == 0 {
		return nil, fmt.Errorf("addrspace: GetModuleHandleW: %w", callErr)
	}
	slide := int64(r) - int64(imageBase)

	f, err := os.Open(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: open %q for reading: %w", exe, err)
	}
	return &ProcessReader{file: f, path: exe, sections: sects, slide: slide}, nil
}

// resolveExePath returns the path to the running executable, falling back
// to os.Args[0] when os.Executable returns "" (observed under Wine).
func resolveExePath() (string, error) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		if len(os.Args) > 0 && os.Args[0] != "" {
			exe = os.Args[0]
		}
	}
	if exe == "" {
		return "", fmt.Errorf("addrspace: cannot determine executable path")
	}
	if !filepath.IsAbs(exe) {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			exe = filepath.Join(wd, exe)
		}
	}
	// Resolve symlinks best-effort; ignore failure (Wine may not support all
	// VFS operations needed by EvalSymlinks).
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	return exe, nil
}

func parsePE(f *pe.File) (uint64, []peSection, error) {
	imageBase, err := peImageBase(f)
	if err != nil {
		return 0, nil, err
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
	return imageBase, sects, nil
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

	// Convert runtime addresses to on-disk preferred virtual addresses
	// by subtracting the ASLR slide.
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
