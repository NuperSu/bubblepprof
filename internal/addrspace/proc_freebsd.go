//go:build freebsd

package addrspace

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcessReader reads bytes from the current process's virtual address
// space. It first tries /proc/self/map + /proc/self/mem (requires procfs to
// be mounted), gating every read against read-only mappings exactly as the
// Linux implementation does. When procfs is unavailable it falls back to
// ELFReader, which reads on-disk PT_LOAD segments; that path is only fully
// correct for non-PIE binaries because no load-bias correction is applied.
type ProcessReader struct {
	maps []Mapping  // populated on procfs path; nil on ELF path
	mem  *os.File   // non-nil when /proc/self/mem is in use
	elf  *ELFReader // non-nil when ELF fallback is in use
	path string
}

// OpenSelfProcessReader opens the best available address-space reader for
// the current process. On the procfs path it parses /proc/self/map to build
// a read-only mapping filter before opening /proc/self/mem, so writable
// mappings (heap, stack) are never served. If procfs is unavailable it falls
// back to ELFReader. Callers must Close the returned reader.
func OpenSelfProcessReader() (*ProcessReader, error) {
	maps, err := readSelfMap()
	if err == nil {
		mem, memErr := os.Open("/proc/self/mem")
		if memErr == nil {
			return &ProcessReader{maps: maps, mem: mem, path: "/proc/self/mem"}, nil
		}
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

// Mappings returns a copy of the readable mappings the reader is aware of on
// the procfs path, or nil on the ELF fallback path. Safe to mutate.
func (r *ProcessReader) Mappings() []Mapping {
	if r == nil {
		return nil
	}
	if len(r.maps) == 0 {
		return nil
	}
	out := make([]Mapping, len(r.maps))
	copy(out, r.maps)
	return out
}

// ReadAtAddr implements Reader. On the procfs path it only serves addresses
// that fall entirely within a single read-only mapping (same policy as Linux).
// On the ELF fallback path addr must match an on-disk PT_LOAD Vaddr, which is
// only correct for non-PIE binaries.
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

	end, ok := AddUint64(addr, size)
	if !ok {
		return nil, false
	}
	for _, m := range r.maps {
		if !mappingEligibleForStringBody(m) {
			continue
		}
		if addr >= m.Start && end <= m.End {
			if addr > math.MaxInt64 {
				return nil, false
			}
			buf := make([]byte, size)
			if _, err := r.mem.ReadAt(buf, int64(addr)); err != nil {
				return nil, false
			}
			return buf, true
		}
	}
	return nil, false
}

func readSelfMap() ([]Mapping, error) {
	f, err := os.Open("/proc/self/map")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseFreeBSDProcMap(f)
}

// parseFreeBSDProcMap parses the contents of /proc/<pid>/map on FreeBSD.
// Each line has the form:
//
//	0x<start> 0x<end> <resident> <private> 0x<obj> <perms> <ref> <shadow> <flags> <cow> <nc> <type> [<path>] ...
//
// Example:
//
//	0x400000 0x41d000 5 0 0xfffff80012abc000 r-x 1 0 0x1000 COW NC vnode /usr/local/bin/foo NCH -1
//	0x41e000 0x41f000 1 1 0xfffff8001abcd000 rw- 1 1 0x0000 COW NNC anon - NCH -1
func parseFreeBSDProcMap(r io.Reader) ([]Mapping, error) {
	var out []Mapping
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		start, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "0x"), 16, 64)
		if err != nil {
			continue
		}
		end, err := strconv.ParseUint(strings.TrimPrefix(fields[1], "0x"), 16, 64)
		if err != nil || end <= start {
			continue
		}
		perms := fields[5]
		if len(perms) < 2 {
			continue
		}
		m := Mapping{
			Start: start,
			End:   end,
			Read:  perms[0] == 'r',
			Write: perms[1] == 'w',
		}
		if len(perms) >= 3 {
			m.Exec = perms[2] == 'x'
		}
		// type field is at index 11; path follows at index 12 for vnode mappings.
		if len(fields) >= 13 && fields[11] == "vnode" {
			m.Path = fields[12]
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
