package heaplabels

import (
	"encoding/binary"
	"sort"

	"bubblepprof/internal/heapsnapshot"
)

type Memory struct {
	ranges []Range
}

type Range struct {
	Start uint64
	End   uint64
	Data  []byte
	Kind  string
}

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

func (m *Memory) Read(addr uint64, size uint64) ([]byte, bool) {
	if m == nil {
		return nil, false
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
			return r.Data[off : off+size], true
		}
	}
	return nil, false
}

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
