package bundle

import (
	"archive/tar"
	"bytes"
	"testing"
)

func TestValidateArchive(t *testing.T) {
	dump := []byte("go1.7 heap dump\n\x00")
	valid := writeTestBundle(t, WriteInput{HeapDump: bytes.NewReader(dump), HeapDumpSize: int64(len(dump)), Segments: []Segment{{Addr: 0x1000, Size: 3, R: bytes.NewReader([]byte("abc"))}}})
	if err := ValidateArchive(bytes.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, valid[:len(valid)-512], valid[:len(valid)-1024], append(append([]byte{}, valid...), 1)} {
		if err := ValidateArchive(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted truncated or trailing bytes")
		}
	}
	bad := writeTestBundle(t, WriteInput{HeapDump: bytes.NewReader([]byte("invalid")), HeapDumpSize: 7})
	if err := ValidateArchive(bytes.NewReader(bad)); err == nil {
		t.Fatal("invalid heap signature")
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	writeMember(tw, MetaMember, []byte(`{"format_version":1}`))
	writeMember(tw, HeapDumpMember, dump)
	writeMember(tw, HeapDumpMember, dump)
	tw.Close()
	if err := ValidateArchive(bytes.NewReader(out.Bytes())); err == nil {
		t.Fatal("duplicate member")
	}
}
