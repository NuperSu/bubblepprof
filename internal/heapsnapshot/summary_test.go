package heapsnapshot

import (
	"bytes"
	"strings"
	"testing"
)

func TestFieldKindString(t *testing.T) {
	cases := map[FieldKind]string{
		FieldKindEol:     "eol",
		FieldKindPtr:     "ptr",
		FieldKindIface:   "iface",
		FieldKindEface:   "eface",
		FieldKind(255):   "unknown",
		FieldKind(99999): "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("FieldKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestPrintSummaryNil(t *testing.T) {
	var buf bytes.Buffer
	var s *HeapSnapshot
	s.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "<nil>") {
		t.Fatalf("expected <nil> output, got %q", buf.String())
	}
}

func TestPrintSummaryPopulated(t *testing.T) {
	snap := &HeapSnapshot{
		Header: "go1.7 heap dump",
		Params: DumpParams{
			BigEndian:    true,
			PtrSize:      8,
			HeapStart:    0x1000,
			HeapEnd:      0x2000,
			GOARCH:       "arm64",
			BuildVersion: "go1.26.3",
			NumCPU:       4,
		},
		Stats: ParseStats{
			ObjectCount:            10,
			ObjectBytes:            1024,
			ObjectPointers:         3,
			TypeCount:              2,
			ItabCount:              1,
			GoroutineCount:         5,
			StackFrameCount:        12,
			StackPointers:          6,
			OtherRootCount:         7,
			DataCount:              2,
			DataPointers:           8,
			BSSCount:               1,
			BSSPointers:            9,
			GlobalRootCount:        4,
			FinalizerCount:         2,
			QueuedFinalizers:       1,
			OSThreadCount:          3,
			DeferCount:             4,
			PanicCount:             0,
			MemProfCount:           5,
			AllocSampleCount:       6,
			UnknownRecords:         1,
			InterfaceFieldsDecoded: 11,
			EfaceFieldsDecoded:     12,
		},
		Warnings: []string{"a warning", "another"},
	}
	var buf bytes.Buffer
	snap.PrintSummary(&buf)
	got := buf.String()
	for _, want := range []string{
		"heap dump header: go1.7 heap dump",
		"goarch: arm64",
		"big endian: true",
		"heap range: 0x1000..0x2000",
		"num cpu: 4",
		"objects: 10",
		"object bytes: 1024",
		"goroutines: 5",
		"stack frames: 12",
		"interface fields decoded: 11",
		"eface fields decoded: 12",
		"warnings: 2",
		"warning: a warning",
		"warning: another",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
