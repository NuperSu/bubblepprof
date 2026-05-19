package heaplabels

import (
	"encoding/binary"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// Memory exposes a heap snapshot's per-object contents as an
// address-indexed byte source. It implements addrspace.Reader (and
// addrspace.NamedReader). Structural label reads (runtime.g, labelMap,
// slice headers, string headers) use a Memory directly; string body
// reads may use a composite that falls through to ExtraStringMemory.
//
// Memory has two source modes. The eager mode (NewMemory) indexes
// snap.Objects[i].Contents directly; the lazy mode (NewMemoryFromReader)
// delegates every address-keyed read to an external addrspace.Reader —
// in practice, the *heapdump.ContentResolver produced by
// heapdump.ParseLazyContents, which fetches object bytes from the dump
// file on demand instead of holding them all in the Go heap.
type Memory struct {
	ranges []Range
	lazy   addrspace.Reader
}

// Range is a contiguous mapping of bytes at virtual addresses
// [Start, End). End is exclusive: Start+len(Data) == End.
type Range struct {
	Start uint64
	End   uint64
	Data  []byte
	Kind  string
}

// NewMemory builds a Memory from a parsed heap snapshot. Objects
// without retained contents are skipped; ranges whose addr+size
// overflows uint64 are skipped defensively.
func NewMemory(snap *heapsnapshot.HeapSnapshot) *Memory {
	m := &Memory{}
	if snap == nil {
		return m
	}
	for _, obj := range snap.Objects {
		if len(obj.Contents) == 0 {
			continue
		}
		end, ok := addUint64(obj.Addr, uint64(len(obj.Contents)))
		if !ok {
			continue
		}
		m.ranges = append(m.ranges, Range{
			Start: obj.Addr,
			End:   end,
			Data:  obj.Contents,
			Kind:  "object",
		})
	}
	sort.Slice(m.ranges, func(i, j int) bool {
		if m.ranges[i].Start == m.ranges[j].Start {
			return m.ranges[i].End < m.ranges[j].End
		}
		return m.ranges[i].Start < m.ranges[j].Start
	})
	return m
}

// NewMemoryFromReader builds a Memory whose address-keyed reads are
// satisfied by r. This is the lazy path: r is expected to be a
// *heapdump.ContentResolver that fetches heap object bytes from the
// dump file on demand. Compared with NewMemory, this avoids retaining
// every object's content bytes in the Go heap.
//
// A nil reader produces a Memory whose Read always returns ok=false.
func NewMemoryFromReader(r addrspace.Reader) *Memory {
	return &Memory{lazy: r}
}

// Read returns the byte slice at [addr, addr+size). It returns ok=true
// with an empty slice on size==0, and ok=false on addr==0 (size>0),
// overflow, or a range that crosses a heap object boundary.
func (m *Memory) Read(addr uint64, size uint64) ([]byte, bool) {
	if m == nil {
		return nil, false
	}
	if m.lazy != nil {
		return m.lazy.ReadAtAddr(addr, size)
	}
	if size == 0 {
		return []byte{}, true
	}
	if addr == 0 {
		return nil, false
	}
	end, ok := addUint64(addr, size)
	if !ok {
		return nil, false
	}
	for _, r := range m.ranges {
		if addr < r.Start {
			return nil, false
		}
		if addr >= r.Start && end <= r.End {
			off := addr - r.Start
			out := make([]byte, size)
			copy(out, r.Data[off:off+size])
			return out, true
		}
	}
	return nil, false
}

// ReadAtAddr implements addrspace.Reader.
func (m *Memory) ReadAtAddr(addr uint64, size uint64) ([]byte, bool) {
	return m.Read(addr, size)
}

// Name implements addrspace.NamedReader. The diagnostic name "heap"
// distinguishes this source from process/elf memory when a Composite
// reports SourceFor.
func (m *Memory) Name() string { return "heap" }

// ReadUintptr decodes a ptrSize-wide unsigned integer at addr.
func (m *Memory) ReadUintptr(addr uint64, ptrSize int, order binary.ByteOrder) (uint64, bool) {
	b, ok := m.Read(addr, uint64(ptrSize))
	if !ok {
		return 0, false
	}
	switch ptrSize {
	case 4:
		return uint64(order.Uint32(b)), true
	case 8:
		return order.Uint64(b), true
	default:
		return 0, false
	}
}

// ReadString reads length bytes at addr as a Go string. length==0 is
// always ok; otherwise the bytes must lie inside a single heap object.
func (m *Memory) ReadString(addr uint64, length uint64) (string, bool) {
	if length == 0 {
		return "", true
	}
	b, ok := m.Read(addr, length)
	if !ok {
		return "", false
	}
	return string(b), true
}

func addUint64(a, b uint64) (uint64, bool) {
	c := a + b
	return c, c >= a
}
