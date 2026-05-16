package addrspace

import (
	"debug/elf"
	"fmt"
	"math"
	"os"
)

// ELFSegment is the file-backed portion of a PT_LOAD segment that
// ELFReader can satisfy reads from.
type ELFSegment struct {
	Vaddr  uint64
	Memsz  uint64
	Filesz uint64
	Off    uint64
	Flags  elf.ProgFlag
}

// ELFReader reads bytes from an ELF executable's PT_LOAD segments at
// the segment's virtual addresses (Vaddr). It recovers label string
// literals from the executable's read-only data when ProcessReader is
// unavailable (e.g. offline analysis).
//
// CAVEAT: Position-independent / ASLR-loaded executables run at a base
// different from their on-disk Vaddrs. Without a load bias from the
// snapshot, raw ELF reading is only reliable for non-PIE binaries.
// /debug/memusage avoids this by using ProcessReader instead.
type ELFReader struct {
	file     *os.File
	path     string
	segments []ELFSegment
}

// OpenELFReader opens path, indexes its readable file-backed PT_LOAD
// segments, and returns a reader. Callers must Close it.
func OpenELFReader(path string) (*ELFReader, error) {
	ef, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("addrspace: parse ELF %q: %w", path, err)
	}
	progs := append([]*elf.Prog(nil), ef.Progs...)
	if cerr := ef.Close(); cerr != nil {
		return nil, fmt.Errorf("addrspace: close ELF %q: %w", path, cerr)
	}

	osf, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("addrspace: open ELF %q: %w", path, err)
	}

	var segs []ELFSegment
	for _, p := range progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if p.Flags&elf.PF_R == 0 {
			continue
		}
		if p.Filesz == 0 {
			continue
		}
		segs = append(segs, ELFSegment{
			Vaddr:  p.Vaddr,
			Memsz:  p.Memsz,
			Filesz: p.Filesz,
			Off:    p.Off,
			Flags:  p.Flags,
		})
	}
	return &ELFReader{file: osf, path: path, segments: segs}, nil
}

// Close releases the underlying executable file.
func (r *ELFReader) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Name implements NamedReader. It returns "elf:<path>".
func (r *ELFReader) Name() string {
	if r == nil {
		return "elf"
	}
	return "elf:" + r.path
}

// Segments returns a copy of the indexed PT_LOAD segments.
func (r *ELFReader) Segments() []ELFSegment {
	if r == nil {
		return nil
	}
	out := make([]ELFSegment, len(r.segments))
	copy(out, r.segments)
	return out
}

// ReadAtAddr implements Reader. It reads only from the file-backed
// part [Vaddr, Vaddr+Filesz) of each segment; addresses inside the
// BSS-only tail [Vaddr+Filesz, Vaddr+Memsz) return false.
func (r *ELFReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
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
	for _, s := range r.segments {
		segEnd, ok := AddUint64(s.Vaddr, s.Filesz)
		if !ok {
			continue
		}
		if addr >= s.Vaddr && end <= segEnd {
			fileOff := s.Off + (addr - s.Vaddr)
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
