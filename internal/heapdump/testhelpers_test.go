package heapdump

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// Helpers used by the synthetic record tests. They mirror the runtime
// dump*() encoders byte-for-byte so test fixtures stay close to the
// real format.

func writeUvarint(w io.Writer, x uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], x)
	w.Write(buf[:n])
}

func writeBool(w io.Writer, b bool) {
	if b {
		writeUvarint(w, 1)
	} else {
		writeUvarint(w, 0)
	}
}

func writeBytes(w io.Writer, b []byte) {
	writeUvarint(w, uint64(len(b)))
	if len(b) > 0 {
		w.Write(b)
	}
}

func writeString(w io.Writer, s string) {
	writeBytes(w, []byte(s))
}

func writeFieldList(w io.Writer, fields []heapsnapshot.Field) {
	for _, f := range fields {
		writeUvarint(w, uint64(f.Kind))
		writeUvarint(w, f.Offset)
	}
	writeUvarint(w, uint64(heapsnapshot.FieldKindEol))
}

func writeHeader(w io.Writer) {
	w.Write([]byte(Header))
	w.Write([]byte{'\n'})
}

// writeParams writes a tagParams record with the supplied params (little
// endian, 8-byte pointers, GOARCH=amd64).
func writeParams(w io.Writer, p heapsnapshot.DumpParams) {
	writeUvarint(w, tagParams)
	writeBool(w, p.BigEndian)
	writeUvarint(w, uint64(p.PtrSize))
	writeUvarint(w, p.HeapStart)
	writeUvarint(w, p.HeapEnd)
	writeString(w, p.GOARCH)
	writeString(w, p.BuildVersion)
	writeUvarint(w, p.NumCPU)
}

// encodePointer encodes a uintptr-sized pointer value into the host byte
// order used by the tests (little endian / 8 bytes).
func encodePointer(value uint64, ptrSize int, byteOrder binary.ByteOrder) []byte {
	buf := make([]byte, ptrSize)
	switch ptrSize {
	case 4:
		byteOrder.PutUint32(buf, uint32(value))
	case 8:
		byteOrder.PutUint64(buf, value)
	}
	return buf
}

// newSyntheticBuffer returns a buffer pre-populated with a valid header
// and standard params. Helpful for record-level tests that don't care
// about endian / ptr size details.
func newSyntheticBuffer() *bytes.Buffer {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		BigEndian:    false,
		PtrSize:      8,
		HeapStart:    0,
		HeapEnd:      0,
		GOARCH:       "amd64",
		BuildVersion: "go-test",
		NumCPU:       4,
	})
	return &buf
}
