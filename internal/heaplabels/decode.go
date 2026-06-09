package heaplabels

import (
	"fmt"
	"sort"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"github.com/NuperSu/bubblepprof/internal/runtimelayout"
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

// DecodeAll decodes pprof labels for every goroutine in snap using the
// supplied runtime layout. The layout must come from runtimelayout.Lookup
// (verified) or runtimelayout.Manual (debug CLI / test); the decoder
// itself never guesses offsets.
//
// Use DecodeAllAuto for the common case where the caller wants the
// verified-table lookup applied implicitly.
func DecodeAll(snap *heapsnapshot.HeapSnapshot, layout runtimelayout.Layout, opts Options) Result {
	opts = normalizeOptions(opts)
	res := Result{
		LabelsByGID: make(map[uint64]map[string]string),
	}
	if snap == nil {
		res.Warnings = append(res.Warnings, "heaplabels: nil heap snapshot")
		return res
	}
	res.Stats.GoroutinesTotal = len(snap.Goroutines)

	mem := opts.HeapMemory
	if mem == nil {
		mem = NewMemory(snap)
	}
	bodyReader := composeStringBodyReader(mem, opts.ExtraStringMemory)
	for _, g := range snap.Goroutines {
		gr := DecodeLabelsForGoroutine(mem, bodyReader, layout, opts, g)
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

// DecodeAllAuto is the convenience wrapper that resolves the layout from
// the verified-runtime table and then calls DecodeAll. When the snapshot
// describes an unsupported runtime, every goroutine is reported with
// StatusUnsupportedRuntime and the matching diagnostic warning.
func DecodeAllAuto(snap *heapsnapshot.HeapSnapshot, opts Options) Result {
	if snap == nil {
		return Result{
			LabelsByGID: make(map[uint64]map[string]string),
			Warnings:    []string{"heaplabels: nil heap snapshot"},
		}
	}
	input := LookupInputFromSnapshot(snap)
	layout, ok := runtimelayout.Lookup(input)
	if !ok {
		return UnsupportedResult(snap, runtimelayout.UnsupportedMessage(input))
	}
	return DecodeAll(snap, layout, opts)
}

// UnsupportedResult builds a Result that reports every goroutine as
// unsupported_runtime with the supplied diagnostic. /debug/memusage uses
// this so the unsupported message is consistent across callers.
func UnsupportedResult(snap *heapsnapshot.HeapSnapshot, message string) Result {
	res := Result{
		LabelsByGID: make(map[uint64]map[string]string),
	}
	if snap == nil {
		if message != "" {
			res.Warnings = append(res.Warnings, message)
		}
		return res
	}
	res.Stats.GoroutinesTotal = len(snap.Goroutines)
	for _, g := range snap.Goroutines {
		res.Goroutines = append(res.Goroutines, GoroutineResult{
			GID:    g.ID,
			GAddr:  g.Addr,
			Status: StatusUnsupportedRuntime,
			Error:  message,
		})
		res.Stats.GoroutinesUnsupported++
	}
	if message != "" {
		res.Warnings = append(res.Warnings, message)
	}
	return res
}

// DecodeLabelsForGoroutine decodes a single goroutine's labels.
//
// structuralReader must cover heap dump object contents and is used for
// all structural reads (runtime.g pointer, labelMap, slice headers,
// string headers). bodyReader is used only for string body bytes and may
// be a composite that falls through to process/ELF memory for literal
// label strings. Pass structuralReader as bodyReader when no fallback is
// needed.
func DecodeLabelsForGoroutine(structuralReader addrspace.Reader, bodyReader addrspace.Reader, layout runtimelayout.Layout, opts Options, g heapsnapshot.Goroutine) GoroutineResult {
	opts = normalizeOptions(opts)
	gr := GoroutineResult{
		GID:   g.ID,
		GAddr: g.Addr,
	}
	if layout.PtrSize != 4 && layout.PtrSize != 8 {
		gr.Status = StatusUnsupportedRuntime
		gr.Error = fmt.Sprintf("unsupported pointer size %d", layout.PtrSize)
		return gr
	}
	labelsFieldAddr, ok := addUint64(g.Addr, layout.GLabelsOffset)
	if !ok {
		gr.Status = StatusMalformed
		gr.Error = "runtime.g.labels field address overflows"
		return gr
	}
	labelsPtr, ok := addrspace.ReadUintptr(structuralReader, labelsFieldAddr, layout.PtrSize, layout.ByteOrder())
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
	labels, err := DecodeLabelMap(structuralReader, bodyReader, layout, opts, labelsPtr)
	if err != nil {
		gr.Status = statusOf(err)
		gr.Error = err.Error()
		return gr
	}
	gr.Status = StatusDecoded
	gr.Labels = labels
	return gr
}

// DecodeLabelMap walks a labelMap pointed to by addr and returns its
// flattened key/value map.
//
// structuralReader covers heap dump object contents and is used for all
// structural reads (labelMap, slice header, label array, string headers).
// bodyReader is used only for string body bytes and may fall through to
// process/ELF memory. Pass structuralReader as bodyReader when no
// fallback is needed.
func DecodeLabelMap(structuralReader addrspace.Reader, bodyReader addrspace.Reader, layout runtimelayout.Layout, opts Options, addr uint64) (map[string]string, error) {
	opts = normalizeOptions(opts)
	order := layout.ByteOrder()
	setAddr, ok := addUint64(addr, layout.LabelMapSetOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "labelMap set address overflows"}
	}
	listHeaderAddr, ok := addUint64(setAddr, layout.SetListOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "label.Set list address overflows"}
	}

	sliceDataAddr, ok := addUint64(listHeaderAddr, layout.SliceDataOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "label.Set list data address overflows"}
	}
	sliceLenAddr, ok := addUint64(listHeaderAddr, layout.SliceLenOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "label.Set list length address overflows"}
	}
	sliceCapAddr, ok := addUint64(listHeaderAddr, layout.SliceCapOffset)
	if !ok {
		return nil, decodeError{StatusMalformed, "label.Set list capacity address overflows"}
	}
	dataPtr, ok := addrspace.ReadUintptr(structuralReader, sliceDataAddr, layout.PtrSize, order)
	if !ok {
		return nil, decodeError{StatusLabelsObjectMissing, "labelMap object bytes are unavailable"}
	}
	length, ok := addrspace.ReadUintptr(structuralReader, sliceLenAddr, layout.PtrSize, order)
	if !ok {
		return nil, decodeError{StatusLabelsObjectMissing, "label.Set list length is unavailable"}
	}
	capacity, ok := addrspace.ReadUintptr(structuralReader, sliceCapAddr, layout.PtrSize, order)
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
	if _, ok := structuralReader.ReadAtAddr(dataPtr, arrayBytes); !ok {
		return nil, decodeError{StatusLabelArrayMissing, "label array bytes are unavailable"}
	}

	labels := make(map[string]string, length)
	for i := uint64(0); i < length; i++ {
		labelAddr, ok := addUint64(dataPtr, i*layout.LabelSize)
		if !ok {
			return nil, decodeError{StatusMalformed, "label address overflows"}
		}
		keyAddr, ok := addUint64(labelAddr, layout.LabelKeyOffset)
		if !ok {
			return nil, decodeError{StatusMalformed, "label key address overflows"}
		}
		valueAddr, ok := addUint64(labelAddr, layout.LabelValueOffset)
		if !ok {
			return nil, decodeError{StatusMalformed, "label value address overflows"}
		}
		key, err := DecodeString(structuralReader, bodyReader, layout, opts, keyAddr)
		if err != nil {
			return nil, err
		}
		value, err := DecodeString(structuralReader, bodyReader, layout, opts, valueAddr)
		if err != nil {
			return nil, err
		}
		labels[key] = value
	}
	return labels, nil
}

// DecodeString reads a Go string header at headerAddr and returns the
// referenced bytes.
//
// structuralReader (heap-only) is used for the string header itself
// (data pointer and length fields). bodyReader is used for the actual
// string bytes and may fall through to process/ELF memory where literal
// label strings reside. Pass structuralReader as bodyReader when no
// fallback is needed.
func DecodeString(structuralReader addrspace.Reader, bodyReader addrspace.Reader, layout runtimelayout.Layout, opts Options, headerAddr uint64) (string, error) {
	opts = normalizeOptions(opts)
	order := layout.ByteOrder()
	strDataAddr, ok := addUint64(headerAddr, layout.StringDataOffset)
	if !ok {
		return "", decodeError{StatusMalformed, "string header data address overflows"}
	}
	strLenAddr, ok := addUint64(headerAddr, layout.StringLenOffset)
	if !ok {
		return "", decodeError{StatusMalformed, "string header length address overflows"}
	}
	dataPtr, ok := addrspace.ReadUintptr(structuralReader, strDataAddr, layout.PtrSize, order)
	if !ok {
		return "", decodeError{StatusStringMissing, "string header data pointer is unavailable"}
	}
	length, ok := addrspace.ReadUintptr(structuralReader, strLenAddr, layout.PtrSize, order)
	if !ok {
		return "", decodeError{StatusStringMissing, "string header length is unavailable"}
	}
	if length > opts.MaxStringLen {
		return "", decodeError{StatusMalformed, fmt.Sprintf("string length %d exceeds max %d", length, opts.MaxStringLen)}
	}
	if length == 0 {
		return "", nil
	}
	if dataPtr == 0 {
		return "", decodeError{StatusStringMissing, "string header data pointer is nil"}
	}
	s, ok := addrspace.ReadString(bodyReader, dataPtr, length, opts.MaxStringLen)
	if !ok {
		return "", decodeError{StatusStringMissing, fmt.Sprintf("string bytes unavailable at 0x%x length %d", dataPtr, length)}
	}
	return s, nil
}

// composeStringBodyReader builds the reader used exclusively for string
// body bytes. When ExtraStringMemory is supplied, a Composite is built
// that prefers heap bytes and falls through to the extra source (e.g.
// process/ELF memory) for addresses outside heap object contents. This
// reader must NOT be used for structural reads (runtime.g, labelMap,
// slice headers, string headers) — use the heap-only Memory directly.
func composeStringBodyReader(mem *Memory, extra addrspace.Reader) addrspace.Reader {
	if extra == nil {
		return mem
	}
	return addrspace.Composite{
		Readers: []addrspace.NamedReader{mem, asNamedReader(extra)},
	}
}

func asNamedReader(r addrspace.Reader) addrspace.NamedReader {
	if r == nil {
		return nil
	}
	if n, ok := r.(addrspace.NamedReader); ok {
		return n
	}
	return unnamedReader{Reader: r}
}

type unnamedReader struct{ addrspace.Reader }

func (unnamedReader) Name() string { return "extra" }

// FindOffsetCandidates is a debug/diagnostic helper used by the
// labeloffsetprobe binary and offline offset probes.
// It scans each goroutine's runtime.g object contents for a pointer that,
// when interpreted as a labelMap address, yields a label map containing
// the requested label key/value pairs.
//
// The scan works on little-endian targets (both 32-bit and 64-bit); on
// big-endian it returns an empty slice. Callers must treat candidates as
// candidates, never as verified offsets.
func FindOffsetCandidates(snap *heapsnapshot.HeapSnapshot, mem *Memory, want map[string]string, opts Options) []OffsetCandidate {
	opts = normalizeOptions(opts)
	if snap == nil || len(want) == 0 {
		return nil
	}
	probeLayout, err := runtimelayout.Manual(LookupInputFromSnapshot(snap), 0)
	if err != nil {
		return nil
	}
	if mem == nil {
		mem = NewMemory(snap)
	}
	order := probeLayout.ByteOrder()
	byOffset := map[uint64]*OffsetCandidate{}
	for _, g := range snap.Goroutines {
		gRange, ok := mem.rangeContaining(g.Addr)
		if !ok {
			continue
		}
		size := gRange.End - g.Addr
		for off := uint64(0); off+uint64(probeLayout.PtrSize) <= size; off += uint64(probeLayout.PtrSize) {
			ptr, ok := mem.ReadUintptr(g.Addr+off, probeLayout.PtrSize, order)
			if !ok || ptr == 0 {
				continue
			}
			labels, err := DecodeLabelMap(mem, mem, probeLayout, opts, ptr)
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

// FindCandidateGLabelsOffsets returns just the offset values from
// FindOffsetCandidates.
func FindCandidateGLabelsOffsets(snap *heapsnapshot.HeapSnapshot, mem *Memory, want map[string]string, opts Options) []uint64 {
	candidates := FindOffsetCandidates(snap, mem, want, opts)
	out := make([]uint64, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Offset)
	}
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
