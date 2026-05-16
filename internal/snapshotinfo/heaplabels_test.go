package snapshotinfo

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"bubblepprof/internal/heaplabels"
	"bubblepprof/internal/snapshot"
)

func TestPrintHeapLabelsDecode(t *testing.T) {
	heap := buildHeapDumpWithLabels()
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})

	var out bytes.Buffer
	err := PrintHeapLabels(&out, path, heapLabelCLIOptions{
		DecodeOptions: heaplabels.Options{GLabelsOffset: 0x18, HasGLabelsOffset: true},
	})
	if err != nil {
		t.Fatalf("PrintHeapLabels: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"runtime.g.labels offset: 0x18",
		"decoded labels: 1",
		"goroutine 123",
		"bubble=alpha",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintHeapLabelsFindOffset(t *testing.T) {
	heap := buildHeapDumpWithLabels()
	path := writeBundleAt(t, snapshot.BundleSource{
		HeapDump:         bytes.NewReader(heap),
		HeapDumpSize:     int64(len(heap)),
		GoroutineProfile: []byte("p"),
		Metadata: snapshot.SnapshotMetadata{
			Format:               snapshot.FormatV1,
			GoVersion:            "go-test",
			HeapDumpFile:         snapshot.HeapDumpFile,
			GoroutineProfileFile: snapshot.GoroutineProfileFile,
		},
	})

	var out bytes.Buffer
	err := PrintHeapLabels(&out, path, heapLabelCLIOptions{
		FindLabels: map[string]string{"bubble": "alpha"},
	})
	if err != nil {
		t.Fatalf("PrintHeapLabels: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"offset discovery:",
		"runtime.g.labels offset: 0x18",
		"using discovered offset: 0x18",
		"bubble=alpha",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestParseFindLabels(t *testing.T) {
	got, err := parseFindLabels("bubble=alpha, job=42")
	if err != nil {
		t.Fatalf("parseFindLabels: %v", err)
	}
	if got["bubble"] != "alpha" || got["job"] != "42" {
		t.Fatalf("labels = %#v", got)
	}

	if _, err := parseFindLabels("bubble"); err == nil {
		t.Fatalf("expected invalid find label")
	}
}

func buildHeapDumpWithLabels() []byte {
	var buf bytes.Buffer
	buf.WriteString("go1.7 heap dump\n")

	writeUvarint(&buf, 6) // tagParams
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 8)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeString(&buf, "amd64")
	writeString(&buf, "go-test")
	writeUvarint(&buf, 1)

	gObj := make([]byte, 0x40)
	putPtr(gObj, 0x18, 0x1000)
	writeObject(&buf, 0x5000, gObj)

	labelMap := make([]byte, 24)
	putPtr(labelMap, 0, 0x2000)
	putPtr(labelMap, 8, 1)
	putPtr(labelMap, 16, 1)
	writeObject(&buf, 0x1000, labelMap)

	labelArray := make([]byte, 32)
	putStringHeader(labelArray, 0, 0x3000, "bubble")
	putStringHeader(labelArray, 16, 0x3010, "alpha")
	writeObject(&buf, 0x2000, labelArray)
	writeObject(&buf, 0x3000, []byte("bubble"))
	writeObject(&buf, 0x3010, []byte("alpha"))

	writeUvarint(&buf, 4) // tagGoroutine
	writeUvarint(&buf, 0x5000)
	writeUvarint(&buf, 0xbb00)
	writeUvarint(&buf, 123)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeString(&buf, "")
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)
	writeUvarint(&buf, 0)

	writeUvarint(&buf, 0) // tagEOF
	return buf.Bytes()
}

func writeObject(buf *bytes.Buffer, addr uint64, contents []byte) {
	writeUvarint(buf, 1) // tagObject
	writeUvarint(buf, addr)
	writeBytes(buf, contents)
	writeFields(buf, nil)
}

func putStringHeader(buf []byte, off int, addr uint64, s string) {
	putPtr(buf, off, addr)
	putPtr(buf, off+8, uint64(len(s)))
}

func putPtr(buf []byte, off int, value uint64) {
	binary.LittleEndian.PutUint64(buf[off:off+8], value)
}
