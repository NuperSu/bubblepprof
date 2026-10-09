package snapshotgraph

import (
	"context"
	"errors"
	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
	"math"
	"testing"
)

type cancellationChecks struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *cancellationChecks) Err() error {
	c.calls++
	if c.calls == 20 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestBuildCancellationAndByteOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := &cancellationChecks{Context: ctx, cancel: cancel}
	snap := &heapsnapshot.HeapSnapshot{Objects: make([]heapsnapshot.Object, 100)}
	for i := range snap.Objects {
		snap.Objects[i] = heapsnapshot.Object{Addr: uint64(i+1) * 16, Size: 8}
	}
	if _, err := BuildContext(checked, snap, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	snap.Objects = []heapsnapshot.Object{{Size: math.MaxUint64}, {Size: 1}}
	if _, err := Build(snap, Options{}); err == nil {
		t.Fatal("total byte overflow accepted")
	}
}
func TestGlobalSlotOverflow(t *testing.T) {
	seg := heapsnapshot.DataSegment{Addr: math.MaxUint64 - 3, Fields: []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 8}, {Kind: heapsnapshot.FieldKindEface, Offset: math.MaxUint64}}, PointerAddrs: []uint64{1, 2}}
	slots := dataSegmentPointerSlots(seg, 8)
	if len(slots) != 2 || slots[0] != 0 || slots[1] != 0 {
		t.Fatal(slots)
	}
}
