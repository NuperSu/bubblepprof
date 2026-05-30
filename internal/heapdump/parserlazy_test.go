package heapdump

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/heapsnapshot"
)

// buildDump returns a *bytes.Reader over a minimal heap dump containing
// the given object records, followed by tagEOF. The reader satisfies both
// io.Reader and io.ReaderAt, which lets us pass it as both arguments to
// ParseLazyContents without copying.
func buildDump(objects []struct {
	addr    uint64
	content []byte
	fields  []heapsnapshot.Field
}) *bytes.Reader {
	var buf bytes.Buffer
	writeHeader(&buf)
	writeParams(&buf, heapsnapshot.DumpParams{
		PtrSize:      8,
		BigEndian:    false,
		GOARCH:       "amd64",
		BuildVersion: "go-test",
		NumCPU:       1,
	})
	for _, obj := range objects {
		writeUvarint(&buf, tagObject)
		writeUvarint(&buf, obj.addr)
		writeBytes(&buf, obj.content)
		writeFieldList(&buf, obj.fields)
	}
	writeUvarint(&buf, tagEOF)
	return bytes.NewReader(buf.Bytes())
}

func TestParseLazyContents_NilRA(t *testing.T) {
	r := buildDump(nil)
	_, _, err := ParseLazyContents(r, nil, Options{})
	if err == nil {
		t.Fatal("expected error when ra is nil")
	}
}

// Contents should be nil on every object — the whole point of lazy parsing.
// PointerAddrs must still be extracted.
func TestParseLazyContents_ContentsNotRetained(t *testing.T) {
	ptr := make([]byte, 8)
	binary.LittleEndian.PutUint64(ptr, 0xdeadbeef00001000) // a pointer value
	content := append(ptr, make([]byte, 8)...)             // 16 bytes total

	r := buildDump([]struct {
		addr    uint64
		content []byte
		fields  []heapsnapshot.Field
	}{
		{
			addr:    0x1000,
			content: content,
			fields:  []heapsnapshot.Field{{Kind: heapsnapshot.FieldKindPtr, Offset: 0}},
		},
	})
	snap, _, err := ParseLazyContents(r, r, Options{})
	if err != nil {
		t.Fatalf("ParseLazyContents: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(snap.Objects))
	}
	obj := snap.Objects[0]
	if obj.Contents != nil {
		t.Fatalf("Contents must be nil in lazy parse, got %d bytes", len(obj.Contents))
	}
	if len(obj.PointerAddrs) != 1 || obj.PointerAddrs[0] != 0xdeadbeef00001000 {
		t.Fatalf("PointerAddrs = %v, want [0xdeadbeef00001000]", obj.PointerAddrs)
	}
}

// KeepObjectContents=true must be silently overridden to false.
func TestParseLazyContents_KeepObjectContentsIgnored(t *testing.T) {
	r := buildDump([]struct {
		addr    uint64
		content []byte
		fields  []heapsnapshot.Field
	}{
		{addr: 0x2000, content: []byte("HELLO_WORLD_12345"), fields: nil},
	})
	snap, _, err := ParseLazyContents(r, r, Options{KeepObjectContents: true})
	if err != nil {
		t.Fatalf("ParseLazyContents: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(snap.Objects))
	}
	if snap.Objects[0].Contents != nil {
		t.Fatal("KeepObjectContents=true must be silently overridden; Contents should be nil")
	}
}

// The resolver must be able to read back the exact bytes stored in the dump
// at a given virtual address, both at the object base and at interior offsets.
func TestParseLazyContents_ResolverReadsContents(t *testing.T) {
	content := []byte("ABCDEFGHIJKLMNOP") // 16 bytes
	r := buildDump([]struct {
		addr    uint64
		content []byte
		fields  []heapsnapshot.Field
	}{
		{addr: 0x4000, content: content, fields: nil},
	})
	snap, resolver, err := ParseLazyContents(r, r, Options{})
	if err != nil {
		t.Fatalf("ParseLazyContents: %v", err)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(snap.Objects))
	}
	if resolver.ObjectCount() != 1 {
		t.Fatalf("resolver.ObjectCount() = %d, want 1", resolver.ObjectCount())
	}

	// Full object read.
	got, ok := resolver.Read(0x4000, 16)
	if !ok {
		t.Fatal("resolver.Read base addr returned ok=false")
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("resolver.Read = %q, want %q", got, content)
	}

	// Interior read.
	got, ok = resolver.Read(0x4004, 4)
	if !ok {
		t.Fatal("resolver.Read interior returned ok=false")
	}
	if !bytes.Equal(got, content[4:8]) {
		t.Fatalf("resolver.Read interior = %q, want %q", got, content[4:8])
	}
}

// Resolver.ObjectCount must match the number of objects in the dump.
func TestParseLazyContents_ObjectCount(t *testing.T) {
	objects := []struct {
		addr    uint64
		content []byte
		fields  []heapsnapshot.Field
	}{
		{addr: 0x1000, content: []byte("aaaa"), fields: nil},
		{addr: 0x2000, content: []byte("bbbbbbbb"), fields: nil},
		{addr: 0x3000, content: []byte("cccc"), fields: nil},
	}
	r := buildDump(objects)
	snap, resolver, err := ParseLazyContents(r, r, Options{})
	if err != nil {
		t.Fatalf("ParseLazyContents: %v", err)
	}
	if len(snap.Objects) != 3 {
		t.Fatalf("snap.Objects = %d, want 3", len(snap.Objects))
	}
	if got := resolver.ObjectCount(); got != 3 {
		t.Fatalf("resolver.ObjectCount() = %d, want 3", got)
	}
}
