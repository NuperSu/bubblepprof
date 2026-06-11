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
// space using one of two sources, in order of preference:
//
//   - procfs (/proc/self/map + /proc/self/mem), when procfs is mounted.
//     Uses live runtime addresses; correct for both PIE and non-PIE binaries.
//     Matches the Linux implementation's read-only mapping gate.
//   - the on-disk ELF executable, when procfs is not mounted. This is the
//     modal path on FreeBSD, where procfs is deprecated and unmounted by
//     default. It reads file-backed PT_LOAD segments at their on-disk Vaddrs
//     and is only correct when runtime addresses equal on-disk Vaddrs — i.e.
//     for non-PIE binaries (no load-bias correction is applied).
//
// Both are real production paths; the ELF source is not a degraded fallback.
type ProcessReader struct {
	maps []Mapping  // populated when procfs is in use; nil otherwise
	mem  *os.File   // non-nil when /proc/self/mem is in use
	elf  *ELFReader // non-nil when the on-disk ELF source is in use
	path string
}

// OpenSelfProcessReader opens an address-space reader for the current
// process. It prefers procfs (parsing /proc/self/map for a read-only
// mapping filter, then opening /proc/self/mem) because that source handles
// PIE binaries at their live runtime addresses; when procfs is not mounted
// — the default on FreeBSD — it opens the on-disk ELF executable instead,
// which is correct for non-PIE binaries. Callers must Close the returned
// reader.
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

// Source returns a human-readable description of which backing source the
// reader is using on this FreeBSD process. It is "/proc/self/mem" when
// procfs is mounted and the reader is using it, or "elf:<path>" when the
// on-disk ELF fallback is in use (correct only for non-PIE binaries).
// Used for diagnostics (e.g. by cmd/labeloffsetprobe) to make the
// FreeBSD configuration explicit.
func (r *ProcessReader) Source() string {
	if r == nil {
		return "<closed>"
	}
	if r.mem != nil {
		return "/proc/self/mem"
	}
	if r.elf != nil {
		if r.path == "" {
			return "elf:<unknown>"
		}
		return "elf:" + r.path
	}
	return "<closed>"
}

// Mappings returns a copy of the readable mappings the reader is aware of
// when using procfs, or nil when using the on-disk ELF source. Safe to mutate.
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

// EligibleStringRanges returns the virtual-address ranges this reader
// would serve ReadAtAddr from, for snapshotting into an
// external-analyser bundle. With procfs these are the read-only
// mappings at live runtime addresses; with the on-disk ELF source they
// are the file-backed PT_LOAD ranges at on-disk Vaddrs (correct only
// for non-PIE binaries, the same constraint as ReadAtAddr).
func (r *ProcessReader) EligibleStringRanges() []Mapping {
	if r == nil {
		return nil
	}
	if r.mem != nil {
		var out []Mapping
		for _, m := range r.maps {
			if mappingEligibleForStringBody(m) {
				out = append(out, m)
			}
		}
		return out
	}
	if r.elf != nil {
		var out []Mapping
		for _, s := range r.elf.Segments() {
			end, ok := AddUint64(s.Vaddr, s.Filesz)
			if !ok {
				continue
			}
			out = append(out, Mapping{
				Start: s.Vaddr,
				End:   end,
				Read:  true,
				Path:  r.path,
			})
		}
		return out
	}
	return nil
}

// ReadAtAddr implements Reader. With procfs it only serves addresses that
// fall entirely within a single read-only mapping (same policy as Linux).
// With the on-disk ELF source addr must lie inside a file-backed PT_LOAD
// segment at its on-disk Vaddr, which is only correct for non-PIE binaries.
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
