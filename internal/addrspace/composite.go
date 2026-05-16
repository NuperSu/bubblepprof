package addrspace

import "strings"

// Composite tries Readers in order and returns the first successful
// read. The intended use for heap-label decoding is:
//
//	Composite{Readers: []NamedReader{heapRangeReader, processReader}}
//
// so heap dump bytes are preferred when present and the process address
// space is only consulted to fill in string literals.
type Composite struct {
	Readers []NamedReader
}

// ReadAtAddr implements Reader.
func (c Composite) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	if _, ok := AddUint64(addr, size); !ok {
		return nil, false
	}
	for _, r := range c.Readers {
		if r == nil {
			continue
		}
		if b, ok := r.ReadAtAddr(addr, size); ok {
			return b, true
		}
	}
	return nil, false
}

// Name returns a diagnostic name like "composite[heap,process]".
func (c Composite) Name() string {
	names := make([]string, 0, len(c.Readers))
	for _, r := range c.Readers {
		if r == nil {
			continue
		}
		names = append(names, r.Name())
	}
	return "composite[" + strings.Join(names, ",") + "]"
}

// SourceFor returns the Name() of the first reader that satisfies a
// read of [addr, addr+size). Useful in tests and diagnostics; not
// required for normal decoding.
func (c Composite) SourceFor(addr uint64, size uint64) (string, bool) {
	if size == 0 {
		return "", true
	}
	if addr == 0 {
		return "", false
	}
	if _, ok := AddUint64(addr, size); !ok {
		return "", false
	}
	for _, r := range c.Readers {
		if r == nil {
			continue
		}
		if _, ok := r.ReadAtAddr(addr, size); ok {
			return r.Name(), true
		}
	}
	return "", false
}
