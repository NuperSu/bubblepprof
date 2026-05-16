package heapdump

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNewReaderWithBufio(t *testing.T) {
	src := bufio.NewReader(strings.NewReader("hi\n"))
	r := newReader(src, Limits{})
	if r.r != src {
		t.Fatal("newReader should reuse the supplied bufio.Reader")
	}
}

func TestUvarintTruncatedFirstByte(t *testing.T) {
	r := newReader(bytes.NewReader(nil), Limits{})
	if _, err := r.Uvarint(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestUvarintTruncatedContinuation(t *testing.T) {
	r := newReader(bytes.NewReader([]byte{0x80, 0x80}), Limits{})
	if _, err := r.Uvarint(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestBytesEmpty(t *testing.T) {
	r := newReader(bytes.NewReader([]byte{0x00}), Limits{})
	got, err := r.Bytes()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestBytesTruncated(t *testing.T) {
	r := newReader(bytes.NewReader(append(encodeUvarint(5), []byte("ab")...)), Limits{})
	_, err := r.Bytes()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestBytesExceedsMaxMemRange(t *testing.T) {
	r := newReader(bytes.NewReader(encodeUvarint(1000)), Limits{MaxMemRangeSize: 8})
	_, err := r.Bytes()
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestStringEmpty(t *testing.T) {
	r := newReader(bytes.NewReader([]byte{0x00}), Limits{})
	got, err := r.String()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestStringLengthError(t *testing.T) {
	r := newReader(bytes.NewReader([]byte{0x80}), Limits{}) // truncated uvarint
	if _, err := r.String(); err == nil {
		t.Fatal("expected error from truncated length")
	}
}

func TestBytesLengthError(t *testing.T) {
	r := newReader(bytes.NewReader([]byte{0x80}), Limits{})
	if _, err := r.Bytes(); err == nil {
		t.Fatal("expected error")
	}
}

func TestFieldListPropagatesEOFFromKind(t *testing.T) {
	r := newReader(bytes.NewReader(nil), Limits{})
	if _, err := r.FieldList(); err == nil {
		t.Fatal("expected error")
	}
}

func TestFieldListPropagatesEOFFromOffset(t *testing.T) {
	// kind=ptr but no offset bytes follow.
	r := newReader(bytes.NewReader([]byte{0x01}), Limits{})
	if _, err := r.FieldList(); err == nil {
		t.Fatal("expected error")
	}
}

func TestReadLineEmptyEOF(t *testing.T) {
	r := newReader(bytes.NewReader(nil), Limits{})
	if _, err := r.readLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestReadLineNoNewline(t *testing.T) {
	r := newReader(strings.NewReader("partial"), Limits{})
	_, err := r.readLine()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestOffsetAdvances(t *testing.T) {
	r := newReader(strings.NewReader("ab"), Limits{})
	if r.Offset() != 0 {
		t.Fatalf("initial offset = %d", r.Offset())
	}
	if _, err := r.readByte(); err != nil {
		t.Fatalf("readByte: %v", err)
	}
	if r.Offset() != 1 {
		t.Fatalf("offset after one byte = %d", r.Offset())
	}
}
