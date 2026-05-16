//go:build linux

package addrspace

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Mapping describes one entry of /proc/<pid>/maps: a contiguous virtual
// address range with its permission and (optional) backing path.
type Mapping struct {
	Start uint64
	End   uint64
	Read  bool
	Write bool
	Exec  bool
	Path  string
}

// ProcessReader reads bytes from the current process's address space
// via /proc/self/mem, gated by the readable mappings reported in
// /proc/self/maps. It is intended for the in-process /debug/memusage
// handler so the heap-label decoder can recover ordinary
// runtime/pprof string literals that live in read-only program data.
type ProcessReader struct {
	maps []Mapping
	mem  *os.File
}

// OpenSelfProcessReader opens /proc/self/mem and parses
// /proc/self/maps. Callers must Close the returned reader.
func OpenSelfProcessReader() (*ProcessReader, error) {
	maps, err := readSelfMaps()
	if err != nil {
		return nil, fmt.Errorf("addrspace: read /proc/self/maps: %w", err)
	}
	mem, err := os.Open("/proc/self/mem")
	if err != nil {
		return nil, fmt.Errorf("addrspace: open /proc/self/mem: %w", err)
	}
	return &ProcessReader{maps: maps, mem: mem}, nil
}

// Close releases the underlying /proc/self/mem file.
func (r *ProcessReader) Close() error {
	if r == nil || r.mem == nil {
		return nil
	}
	err := r.mem.Close()
	r.mem = nil
	return err
}

// Name implements NamedReader.
func (r *ProcessReader) Name() string { return "process" }

// Mappings returns a copy of the readable mappings the reader is aware
// of. Useful in diagnostics; the slice is safe to mutate.
func (r *ProcessReader) Mappings() []Mapping {
	if r == nil {
		return nil
	}
	out := make([]Mapping, len(r.maps))
	copy(out, r.maps)
	return out
}

// ReadAtAddr implements Reader. It only succeeds when the requested
// [addr, addr+size) range lies entirely inside a single readable
// mapping; cross-mapping reads return false.
func (r *ProcessReader) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if r == nil || r.mem == nil {
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
	for _, m := range r.maps {
		// Only serve read-only mappings (rodata, text). Writable mappings
		// (heap, stack, anonymous RW) must never supply structural label
		// bytes — they represent live mutable state, not the stop-the-world
		// snapshot, and string body reads from heap/stack are already
		// covered by the heap dump object contents.
		if !m.Read || m.Write {
			continue
		}
		if addr >= m.Start && end <= m.End {
			buf := make([]byte, size)
			if _, err := r.mem.ReadAt(buf, int64(addr)); err != nil {
				return nil, false
			}
			return buf, true
		}
	}
	return nil, false
}

func readSelfMaps() ([]Mapping, error) {
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseProcMaps(f)
}

// parseProcMaps parses the contents of /proc/<pid>/maps. Exported only
// for tests in this package.
func parseProcMaps(r io.Reader) ([]Mapping, error) {
	var out []Mapping
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// Expected line:
		//   start-end perms offset dev inode pathname
		// e.g.
		//   55a0a4c00000-55a0a4c10000 r-xp 00000000 fd:01 12345 /usr/bin/foo
		fields := strings.SplitN(line, " ", 6)
		if len(fields) < 2 {
			continue
		}
		rng := fields[0]
		perms := fields[1]
		dash := strings.IndexByte(rng, '-')
		if dash < 0 {
			continue
		}
		start, err := strconv.ParseUint(rng[:dash], 16, 64)
		if err != nil {
			continue
		}
		end, err := strconv.ParseUint(rng[dash+1:], 16, 64)
		if err != nil || end <= start {
			continue
		}
		if len(perms) < 3 {
			continue
		}
		m := Mapping{
			Start: start,
			End:   end,
			Read:  perms[0] == 'r',
			Write: perms[1] == 'w',
			Exec:  perms[2] == 'x',
		}
		if len(fields) >= 6 {
			m.Path = strings.TrimSpace(fields[5])
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
