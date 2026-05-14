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
// for every non-zero pointer target decoded (used for global root attribution).
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
			if ptr != 0 {
				if slots != nil {
					*slots = append(*slots, containerAddr+f.Offset)
				}
				if targets != nil {
					*targets = append(*targets, ptr)
				}
			}
		case heapsnapshot.FieldKindIface, heapsnapshot.FieldKindEface:
			// Phase 3 preserves iface/eface fields but does not decode them.
			// Whether the data word is a pointer depends on runtime type
			// metadata, so guessing here would create false roots.
			continue
		default:
			if warn != nil {
				warn(fmt.Sprintf("%s: unknown field kind %d at offset %d", context, f.Kind, f.Offset))
			}
		}
	}
}

func readPointer(contents []byte, offset uint64, ptrSize int, byteOrder binary.ByteOrder) (uint64, bool) {
	size := uint64(len(contents))
	if offset > size {
		return 0, false
	}
	if uint64(ptrSize) > size-offset {
		return 0, false
	}
	end := offset + uint64(ptrSize)
	slot := contents[offset:end]
	switch ptrSize {
	case 4:
		return uint64(byteOrder.Uint32(slot)), true
	case 8:
		return byteOrder.Uint64(slot), true
	}
	return 0, false
}
