package heapdump

import (
	"bytes"
	"math"
	"testing"
)

// fakeReaderAt is a deterministic io.ReaderAt over a fixed byte slice.
type fakeReaderAt struct {
	data []byte
}

func (f *fakeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(f.data)) {
		return 0, bytes.ErrTooLarge
	}
	n := copy(p, f.data[off:])
	return n, nil
}

func TestContentResolverRead(t *testing.T) {
	// Lay out two objects in a fake file:
	//   addr=0x1000, len=8, fileOff=10, bytes = "AAAAAAAA"
	//   addr=0x2000, len=4, fileOff=30, bytes = "BBBB"
	src := &fakeReaderAt{data: append(append([]byte("PADPADPADP"), bytes.Repeat([]byte{'A'}, 8)...), append(bytes.Repeat([]byte{'P'}, 12), bytes.Repeat([]byte{'B'}, 4)...)...)}
	if len(src.data) < 34 {
		t.Fatalf("test setup: data too short (%d)", len(src.data))
	}
	c := &ContentResolver{src: src}
	c.record(0x1000, 10, 8)
	c.record(0x2000, 30, 4)
	c.finalize()

	t.Run("exact base address", func(t *testing.T) {
		got, ok := c.Read(0x1000, 8)
		if !ok {
			t.Fatal("Read(0x1000, 8) returned ok=false")
		}
		if !bytes.Equal(got, bytes.Repeat([]byte{'A'}, 8)) {
			t.Fatalf("got %q, want %q", got, bytes.Repeat([]byte{'A'}, 8))
		}
	})

	t.Run("interior read mid-object", func(t *testing.T) {
		got, ok := c.Read(0x1004, 2)
		if !ok {
			t.Fatal("interior read returned ok=false")
		}
		if !bytes.Equal(got, []byte{'A', 'A'}) {
			t.Fatalf("got %q, want %q", got, []byte{'A', 'A'})
		}
	})

	t.Run("crosses object boundary returns false", func(t *testing.T) {
		// 0x1006 + 4 = 0x100A which exceeds the 8-byte object.
		if _, ok := c.Read(0x1006, 4); ok {
			t.Fatal("expected ok=false for cross-boundary read")
		}
	})

	t.Run("addr below any object returns false", func(t *testing.T) {
		if _, ok := c.Read(0x500, 4); ok {
			t.Fatal("expected ok=false for addr below all ranges")
		}
	})

	t.Run("addr in gap between objects returns false", func(t *testing.T) {
		if _, ok := c.Read(0x1100, 4); ok {
			t.Fatal("expected ok=false for addr in inter-object gap")
		}
	})

	t.Run("size=0 always ok", func(t *testing.T) {
		got, ok := c.Read(0x1000, 0)
		if !ok || len(got) != 0 {
			t.Fatalf("got (%v, %v), want ([], true)", got, ok)
		}
	})

	t.Run("addr=0 with size>0 returns false", func(t *testing.T) {
		if _, ok := c.Read(0, 4); ok {
			t.Fatal("expected ok=false for addr=0")
		}
	})

	t.Run("addr+size overflow returns false", func(t *testing.T) {
		if _, ok := c.Read(math.MaxUint64-2, 8); ok {
			t.Fatal("expected ok=false for overflow")
		}
	})

	t.Run("second object lookup via byAddr fast path", func(t *testing.T) {
		got, ok := c.Read(0x2000, 4)
		if !ok {
			t.Fatal("Read(0x2000, 4) returned ok=false")
		}
		if !bytes.Equal(got, []byte("BBBB")) {
			t.Fatalf("got %q, want %q", got, "BBBB")
		}
	})

	t.Run("ReadAtAddr matches Read", func(t *testing.T) {
		got, ok := c.ReadAtAddr(0x2000, 4)
		if !ok || !bytes.Equal(got, []byte("BBBB")) {
			t.Fatalf("ReadAtAddr got (%q, %v)", got, ok)
		}
	})
}

func TestContentResolverNilSafe(t *testing.T) {
	var c *ContentResolver
	if _, ok := c.Read(0x1000, 4); ok {
		t.Fatal("nil resolver should not satisfy reads")
	}
	if got := c.ObjectCount(); got != 0 {
		t.Fatalf("nil resolver ObjectCount = %d, want 0", got)
	}
}
