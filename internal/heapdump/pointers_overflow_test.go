package heapdump

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

func TestPointerProvenanceRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		field heapsnapshot.Field
		base  uint64
	}{
		{heapsnapshot.Field{Kind: heapsnapshot.FieldKindIface, Offset: math.MaxUint64}, 0},
		{heapsnapshot.Field{Kind: heapsnapshot.FieldKindEface, Offset: math.MaxUint64}, 0},
		{heapsnapshot.Field{Kind: heapsnapshot.FieldKindPtr, Offset: 4}, math.MaxUint64 - 2},
	} {
		var targets, slots []uint64
		warnings := 0
		contents := make([]byte, 32)
		for i := range contents {
			contents[i] = 1
		}
		extractPointers(contents, []heapsnapshot.Field{tc.field}, 8, binary.LittleEndian, tc.base, "test", &targets, &slots, func(string) { warnings++ })
		if warnings == 0 || len(targets) != 0 || len(slots) != 0 {
			t.Fatalf("%+v targets=%v slots=%v warnings=%d", tc, targets, slots, warnings)
		}
	}
}
