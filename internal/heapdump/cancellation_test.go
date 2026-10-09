package heapdump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type cancelingReader struct {
	src    io.Reader
	cancel context.CancelFunc
	read   int
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	n, e := r.src.Read(p)
	r.read += n
	if r.read > 64<<10 {
		r.cancel()
	}
	return n, e
}
func TestParsingCancellationAndBoundedHeader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(strings.NewReader(Header+"\n\x00"), Options{Context: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagObject)
	writeUvarint(buf, 0x1000)
	writeBytes(buf, make([]byte, 256<<10))
	writeUvarint(buf, 0)
	writeUvarint(buf, tagEOF)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	src := &cancelingReader{src: bytes.NewReader(buf.Bytes()), cancel: cancel}
	if _, err := Parse(src, Options{Context: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if src.read >= buf.Len() {
		t.Fatal("cancellation read entire dump")
	}
	if _, err := Parse(strings.NewReader(strings.Repeat("x", 1<<20)), Options{}); err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatal(err)
	}
}
func TestObjectRangeOverflow(t *testing.T) {
	buf := newSyntheticBuffer()
	writeUvarint(buf, tagObject)
	writeUvarint(buf, ^uint64(0))
	writeBytes(buf, []byte{1, 2})
	writeUvarint(buf, 0)
	writeUvarint(buf, tagEOF)
	if _, err := Parse(buf, Options{}); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatal(err)
	}
}
