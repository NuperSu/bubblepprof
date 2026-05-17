//go:build freebsd

package addrspace

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ProcessReader reads bytes from the current process's virtual address
// space. It first tries /proc/self/mem (requires procfs to be mounted),
// which uses runtime virtual addresses directly with no ASLR correction.
// When procfs is unavailable it falls back to ELFReader, which reads
// on-disk PT_LOAD segments; that path is only fully correct for non-PIE
// binaries because no load-bias correction is applied.
//
// Unlike the Linux implementation, no /proc/<pid>/map eligibility filter
// is applied on the procfs path: FreeBSD's procmap format differs from
// Linux and the pprof label recovery use-case does not warrant parsing it.
type ProcessReader struct {
	mem  *os.File   // non-nil when /proc/self/mem is in use
	elf  *ELFReader // non-nil when ELF fallback is in use
	path string
}

// OpenSelfProcessReader opens the best available address-space reader for
// the current process. It first attempts /proc/self/mem; if procfs is not
// mounted it opens the executable as an ELFReader. Callers must Close it.
func OpenSelfProcessReader() (*ProcessReader, error) {
	if f, err := os.Open("/proc/self/mem"); err == nil {
		return &ProcessReader{mem: f, path: "/proc/self/mem"}, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("addrspace: determine executable path: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	er, err := OpenELFReader(exe)
	if err != nil {
		return nil, fmt.Errorf("addrspace: %w", err)
	}
	return &ProcessReader{elf: er, path: exe}, nil
}

// Close releases the underlying file descriptor.
func (r *ProcessReader) Close() error {
	if r == nil {
		return nil
	}
	if r.mem != nil {
		err := r.mem.Close()
		r.mem = nil
		return err
	}
	if r.elf != nil {
		err := r.elf.Close()
		r.elf = nil
		return err
	}
	return nil
}

// Name implements NamedReader.
func (r *ProcessReader) Name() string { return "process" }

// Mappings returns nil on FreeBSD; eligibility is enforced per-read.
func (r *ProcessReader) Mappings() []Mapping {
	if r == nil {
		return nil
	}
	return nil
}

// ReadAtAddr implements Reader. On the /proc/self/mem path addr is used
// directly (no ASLR correction needed). On the ELF fallback path addr must
// match the on-disk PT_LOAD Vaddr, which is only correct for non-PIE binaries.
func (r *ProcessReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	if _, ok := AddUint64(addr, size); !ok {
		return nil, false
	}

	if r.elf != nil {
		return r.elf.ReadAtAddr(addr, size)
	}
	if r.mem == nil {
		return nil, false
	}
	if addr > math.MaxInt64 {
		return nil, false
	}
	buf := make([]byte, size)
	if _, err := r.mem.ReadAt(buf, int64(addr)); err != nil {
		return nil, false
	}
	return buf, true
}
