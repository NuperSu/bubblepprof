package heapdump

import (
	"encoding/binary"
	"fmt"

	"bubblepprof/internal/heapsnapshot"
)

// extractPointers decodes pointer values out of a contiguous memory range
// using the field list emitted by the runtime.
//
// `slotAddrs`, if non-nil, will be appended with (containerAddr + offset)
// for every pointer slot decoded (used for global root attribution).
// `targets`, if non-nil, will be appended with the decoded pointer values
// (zero values are skipped).
//
// Out-of-bounds offsets and unsupported field kinds become warnings
// appended via warn rather than fatal errors so the rest of the dump can
// still be parsed.
func extractPointers(
	contents []byte,
	fields []heapsnapshot.Field,
	ptrSize int,
	byteOrder binary.ByteOrder,
	containerAddr uint64,
	context string,
	targets *[]uint64,
	slots *[]uint64,
	warn func(string),
) {
	if ptrSize != 4 && ptrSize != 8 {
		if warn != nil {
			warn(fmt.Sprintf("%s: unsupported ptr size %d", context, ptrSize))
		}
		return
	}

	for _, f := range fields {
		switch f.Kind {
		case heapsnapshot.FieldKindPtr:
			ptr, ok := readPointer(contents, f.Offset, ptrSize, byteOrder)
			if !ok {
				if warn != nil {
					warn(fmt.Sprintf("%s: pointer field offset %d out of bounds for size %d", context, f.Offset, len(contents)))
				}
				continue
			}
			if slots != nil {
				*slots = append(*slots, containerAddr+f.Offset)
			}
			if ptr != 0 && targets != nil {
				*targets = append(*targets, ptr)
			}
		case heapsnapshot.FieldKindIface, heapsnapshot.FieldKindEface:
			// Phase 3 does not try to decode the iface/eface payloads.
			// Their concrete pointer lives at offset+ptrSize and is reached
			// via the itab/type. We still record the data slot so future
			// phases can resolve it. Skip silently when out of bounds.
			if int(f.Offset)+2*ptrSize > len(contents) {
				if warn != nil {
					warn(fmt.Sprintf("%s: %s field at offset %d out of bounds for size %d", context, f.Kind, f.Offset, len(contents)))
				}
				continue
			}
			// Read the data word; it may be a direct pointer for non-indirect
			// interfaces. Skip when the bit-pattern is clearly not a pointer
			// is left to later phases — for now we just store the raw value
			// so callers can decide.
			ptr, ok := readPointer(contents, f.Offset+uint64(ptrSize), ptrSize, byteOrder)
			if !ok {
				continue
			}
			if slots != nil {
				*slots = append(*slots, containerAddr+f.Offset+uint64(ptrSize))
			}
			if ptr != 0 && targets != nil {
				*targets = append(*targets, ptr)
			}
		default:
			if warn != nil {
				warn(fmt.Sprintf("%s: unknown field kind %d at offset %d", context, f.Kind, f.Offset))
			}
		}
	}
}

func readPointer(contents []byte, offset uint64, ptrSize int, byteOrder binary.ByteOrder) (uint64, bool) {
	end := offset + uint64(ptrSize)
	if end > uint64(len(contents)) {
		return 0, false
	}
	slot := contents[offset:end]
	switch ptrSize {
	case 4:
		return uint64(byteOrder.Uint32(slot)), true
	case 8:
		return byteOrder.Uint64(slot), true
	}
	return 0, false
}
