package heaplabels

import (
	"fmt"
	"sort"

	"bubblepprof/internal/heapsnapshot"
)

type decodeError struct {
	status DecodeStatus
	msg    string
}

func (e decodeError) Error() string { return e.msg }

func statusOf(err error) DecodeStatus {
	if e, ok := err.(decodeError); ok {
		return e.status
	}
	return StatusMalformed
}

func DecodeAll(snap *heapsnapshot.HeapSnapshot, opts Options) Result {
	opts = normalizeOptions(opts)
	res := Result{
		LabelsByGID: make(map[uint64]map[string]string),
	}
	if snap == nil {
		res.Warnings = append(res.Warnings, "heaplabels: nil heap snapshot")
		return res
	}
	res.Stats.GoroutinesTotal = len(snap.Goroutines)

	if !opts.HasGLabelsOffset {
		for _, g := range snap.Goroutines {
			gr := GoroutineResult{
				GID:    g.ID,
				GAddr:  g.Addr,
				Status: StatusUnsupportedRuntime,
				Error:  "runtime.g.labels offset is not configured",
			}
			res.Goroutines = append(res.Goroutines, gr)
			res.Stats.GoroutinesUnsupported++
		}
		return res
	}

	layout, ok := LayoutFromSnapshot(snap, opts.GLabelsOffset)
	if !ok {
		for _, g := range snap.Goroutines {
			gr := GoroutineResult{
				GID:    g.ID,
				GAddr:  g.Addr,
				Status: StatusUnsupportedRuntime,
				Error:  fmt.Sprintf("unsupported pointer size %d", snap.Params.PtrSize),
			}
			res.Goroutines = append(res.Goroutines, gr)
			res.Stats.GoroutinesUnsupported++
		}
		return res
	}

	mem := NewMemory(snap)
	for _, g := range snap.Goroutines {
		gr := DecodeLabelsForGoroutine(mem, layout, opts, g)
		res.Goroutines = append(res.Goroutines, gr)
		switch gr.Status {
		case StatusDecoded:
			res.Stats.GoroutinesDecoded++
			res.Stats.LabelsTotal += len(gr.Labels)
			if len(gr.Labels) > 0 {
				res.LabelsByGID[gr.GID] = copyLabels(gr.Labels)
			}
		case StatusNoLabels:
			res.Stats.GoroutinesNoLabels++
		case StatusUnsupportedRuntime:
			res.Stats.GoroutinesUnsupported++
		default:
			res.Stats.GoroutinesFailed++
			if gr.Status == StatusStringMissing {
				res.Stats.StringsMissing++
			}
		}
	}
	return res
}

func DecodeLabelsForGoroutine(mem *Memory, layout Layout, opts Options, g heapsnapshot.Goroutine) GoroutineResult {
	opts = normalizeOptions(opts)
	gr := GoroutineResult{
		GID:   g.ID,
		GAddr: g.Addr,
	}
	labelsFieldAddr, ok := addUint64(g.Addr, layout.GLabelsOffset)
	if !ok {
		gr.Status = StatusMalformed
		gr.Error = "runtime.g.labels field address overflows"
		return gr
	}
	labelsPtr, ok := mem.ReadUintptr(labelsFieldAddr, layout.PtrSize, layout.Order)
	if !ok {
		gr.Status = StatusGObjectMissing
		gr.Error = "runtime.g object bytes are unavailable"
		return gr
	}
	gr.LabelsPtr = labelsPtr
	if labelsPtr == 0 {
		gr.Status = StatusNoLabels
		return gr
	}
	labels, err := DecodeLabelMap(mem, layout, opts, labelsPtr)
	if err != nil {
		gr.Status = statusOf(err)
		gr.Error = err.Error()
		return gr
	}
	gr.Status = StatusDecoded
	gr.Labels = labels
	return gr
}

func DecodeLabelMap(mem *Memory, layout Layout, opts Options, addr uint64) (map[string]string, error) {
	opts = normalizeOptions(opts)
	setAddr, ok := addUint64(addr, layout.LabelMapSetOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "labelMap set address overflows"}
	}
	listHeaderAddr, ok := addUint64(setAddr, layout.SetListOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "label.Set list address overflows"}
	}

	dataPtr, ok := mem.ReadUintptr(listHeaderAddr+layout.SliceDataOffset, layout.PtrSize, layout.Order)
	if !ok {
		return nil, decodeError{StatusLabelsObjectMissing, "labelMap object bytes are unavailable"}
	}
	length, ok := mem.ReadUintptr(listHeaderAddr+layout.SliceLenOffset, layout.PtrSize, layout.Order)
	if !ok {
		return nil, decodeError{StatusLabelsObjectMissing, "label.Set list length is unavailable"}
	}
	capacity, ok := mem.ReadUintptr(listHeaderAddr+layout.SliceCapOffset, layout.PtrSize, layout.Order)
	if !ok {
		return nil, decodeError{StatusLabelsObjectMissing, "label.Set list capacity is unavailable"}
	}
	if length > capacity {
		return nil, decodeError{StatusMalformed, fmt.Sprintf("label.Set list len %d exceeds cap %d", length, capacity)}
	}
	if length > opts.MaxLabels {
		return nil, decodeError{StatusMalformed, fmt.Sprintf("label.Set list len %d exceeds max %d", length, opts.MaxLabels)}
	}
	if length == 0 {
		return map[string]string{}, nil
	}
	if dataPtr == 0 {
		return nil, decodeError{StatusLabelArrayMissing, "label.Set list data pointer is nil"}
	}
	arrayBytes, ok := mulUint64(length, layout.LabelSize)
	if !ok {
		return nil, decodeError{StatusMalformed, "label array size overflows"}
	}
	if _, ok := mem.Read(dataPtr, arrayBytes); !ok {
		return nil, decodeError{StatusLabelArrayMissing, "label array bytes are unavailable"}
	}

	labels := make(map[string]string, length)
	for i := uint64(0); i < length; i++ {
		labelAddr, ok := addUint64(dataPtr, i*layout.LabelSize)
		if !ok {
			return nil, decodeError{StatusMalformed, "label address overflows"}
		}
		key, err := DecodeString(mem, layout, opts, labelAddr+layout.LabelKeyOffset)
		if err != nil {
			return nil, err
		}
		value, err := DecodeString(mem, layout, opts, labelAddr+layout.LabelValueOffset)
		if err != nil {
			return nil, err
		}
		labels[key] = value
	}
	return labels, nil
}

func DecodeString(mem *Memory, layout Layout, opts Options, headerAddr uint64) (string, error) {
	opts = normalizeOptions(opts)
	dataPtr, ok := mem.ReadUintptr(headerAddr+layout.StringDataOffset, layout.PtrSize, layout.Order)
	if !ok {
		return "", decodeError{StatusStringMissing, "string header data pointer is unavailable"}
	}
	length, ok := mem.ReadUintptr(headerAddr+layout.StringLenOffset, layout.PtrSize, layout.Order)
	if !ok {
		return "", decodeError{StatusStringMissing, "string header length is unavailable"}
	}
	if length > opts.MaxStringLen {
		return "", decodeError{StatusMalformed, fmt.Sprintf("string length %d exceeds max %d", length, opts.MaxStringLen)}
	}
	if length == 0 {
		return "", nil
	}
	s, ok := mem.ReadString(dataPtr, length)
	if !ok {
		return "", decodeError{StatusStringMissing, fmt.Sprintf("string bytes unavailable at 0x%x length %d", dataPtr, length)}
	}
	return s, nil
}

func FindCandidateGLabelsOffsets(snap *heapsnapshot.HeapSnapshot, mem *Memory, want map[string]string, opts Options) []uint64 {
	candidates := FindOffsetCandidates(snap, mem, want, opts)
	out := make([]uint64, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Offset)
	}
	return out
}

func FindOffsetCandidates(snap *heapsnapshot.HeapSnapshot, mem *Memory, want map[string]string, opts Options) []OffsetCandidate {
	opts = normalizeOptions(opts)
	if snap == nil || len(want) == 0 {
		return nil
	}
	layout, ok := LayoutFromSnapshot(snap, 0)
	if !ok {
		return nil
	}
	if mem == nil {
		mem = NewMemory(snap)
	}
	byOffset := map[uint64]*OffsetCandidate{}
	for _, g := range snap.Goroutines {
		gRange, ok := mem.rangeContaining(g.Addr)
		if !ok {
			continue
		}
		size := gRange.End - g.Addr
		for off := uint64(0); off+uint64(layout.PtrSize) <= size; off += uint64(layout.PtrSize) {
			ptr, ok := mem.ReadUintptr(g.Addr+off, layout.PtrSize, layout.Order)
			if !ok || ptr == 0 {
				continue
			}
			labels, err := DecodeLabelMap(mem, layout, opts, ptr)
			if err != nil || !containsLabels(labels, want) {
				continue
			}
			c := byOffset[off]
			if c == nil {
				c = &OffsetCandidate{Offset: off}
				byOffset[off] = c
			}
			c.Matches++
			c.GoroutineIDs = append(c.GoroutineIDs, g.ID)
		}
	}
	out := make([]OffsetCandidate, 0, len(byOffset))
	for _, c := range byOffset {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	return out
}

func (m *Memory) rangeContaining(addr uint64) (Range, bool) {
	if m == nil || addr == 0 {
		return Range{}, false
	}
	for _, r := range m.ranges {
		if addr >= r.Start && addr < r.End {
			return r, true
		}
	}
	return Range{}, false
}

func containsLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func mulUint64(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > ^uint64(0)/b {
		return 0, false
	}
	return a * b, true
}

func copyLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
