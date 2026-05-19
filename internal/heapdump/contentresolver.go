package heapdump

import (
	"io"
	"sort"
)

// ContentResolver lazily fetches heap object content bytes from the
// original dump file. ParseLazyContents records (addr -> file offset,
// length) for every heap object record it sees; consumers (notably
// internal/heaplabels) Read bytes by virtual address and the resolver
// translates the lookup into an io.ReaderAt call on the underlying file.
//
// A ContentResolver does not own the underlying io.ReaderAt. The caller
// must keep it open and valid for the resolver's lifetime.
type ContentResolver struct {
	src    io.ReaderAt
	byAddr map[uint64]int
	refs   []contentRef
}

type contentRef struct {
	addr    uint64
	fileOff int64
	length  uint64
}

// ObjectCount reports the number of heap objects indexed by the resolver.
func (c *ContentResolver) ObjectCount() int {
	if c == nil {
		return 0
	}
	return len(c.refs)
}

// Read returns size bytes at virtual address addr. Reads spanning the
// payload of a single heap object are supported, including interior
// addresses (addr strictly greater than the containing object's base).
// Cross-object reads, addr==0 with size>0, and out-of-bounds requests
// return ok=false.
func (c *ContentResolver) Read(addr, size uint64) ([]byte, bool) {
	if c == nil || c.src == nil {
		return nil, false
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end := addr + size
	if end < addr {
		return nil, false
	}
	if i, ok := c.byAddr[addr]; ok {
		return c.readRef(c.refs[i], 0, size)
	}
	j := sort.Search(len(c.refs), func(i int) bool {
		return c.refs[i].addr > addr
	})
	if j == 0 {
		return nil, false
	}
	ref := c.refs[j-1]
	if addr < ref.addr {
		return nil, false
	}
	if end > ref.addr+ref.length {
		return nil, false
	}
	return c.readRef(ref, addr-ref.addr, size)
}

func (c *ContentResolver) readRef(ref contentRef, offsetWithinObject, size uint64) ([]byte, bool) {
	if offsetWithinObject+size > ref.length || offsetWithinObject+size < offsetWithinObject {
		return nil, false
	}
	buf := make([]byte, size)
	n, err := c.src.ReadAt(buf, ref.fileOff+int64(offsetWithinObject))
	if err != nil && err != io.EOF {
		return nil, false
	}
	if uint64(n) != size {
		return nil, false
	}
	return buf, true
}

// ReadAtAddr matches the addrspace.Reader signature so a ContentResolver
// can plug into the heaplabels composite reader chain.
func (c *ContentResolver) ReadAtAddr(addr, size uint64) ([]byte, bool) {
	return c.Read(addr, size)
}

// Name identifies this source in diagnostic output. Matches the
// addrspace.NamedReader convention used elsewhere in the codebase.
func (c *ContentResolver) Name() string { return "heap-lazy" }

func (c *ContentResolver) record(addr uint64, fileOff int64, length uint64) {
	c.refs = append(c.refs, contentRef{addr: addr, fileOff: fileOff, length: length})
}

func (c *ContentResolver) finalize() {
	sort.Slice(c.refs, func(i, j int) bool { return c.refs[i].addr < c.refs[j].addr })
	if c.byAddr == nil {
		c.byAddr = make(map[uint64]int, len(c.refs))
	}
	for i, ref := range c.refs {
		c.byAddr[ref.addr] = i
	}
}
