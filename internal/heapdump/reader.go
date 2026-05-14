package heapdump

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"bubblepprof/internal/heapsnapshot"
)

// reader is a primitive stream reader for the heap dump binary format.
// All integer fields in the dump are encoded as uvarints, strings and
// memory ranges are length-prefixed by uvarints, and bools are 0/1
// uvarints. See runtime/heapdump.go for the source of truth.
type reader struct {
	r      *bufio.Reader
	limits Limits
	offset int64
}

// Limits configures conservative safety bounds for length-prefixed fields.
// A zero value disables the corresponding limit.
type Limits struct {
	MaxStringBytes  uint64
	MaxMemRangeSize uint64
}

func newReader(src io.Reader, limits Limits) *reader {
	if br, ok := src.(*bufio.Reader); ok {
		return &reader{r: br, limits: limits}
	}
	return &reader{r: bufio.NewReader(src), limits: limits}
}

// Offset returns the approximate byte offset of the next byte to be read.
// It is best-effort and intended for error messages.
func (r *reader) Offset() int64 { return r.offset }

func (r *reader) readByte() (byte, error) {
	b, err := r.r.ReadByte()
	if err == nil {
		r.offset++
	}
	return b, err
}

func (r *reader) readFull(buf []byte) error {
	n, err := io.ReadFull(r.r, buf)
	r.offset += int64(n)
	return err
}

// Uvarint reads a variable-length unsigned integer.
func (r *reader) Uvarint() (uint64, error) {
	var x uint64
	var shift uint
	for i := 0; i < 10; i++ {
		b, err := r.readByte()
		if err != nil {
			if err == io.EOF && i > 0 {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		if b < 0x80 {
			if i == 9 && b > 1 {
				return 0, errors.New("uvarint overflow")
			}
			return x | uint64(b)<<shift, nil
		}
		x |= uint64(b&0x7f) << shift
		shift += 7
	}
	return 0, errors.New("uvarint overflow")
}

// Bool reads a single boolean encoded as a 0/1 uvarint.
func (r *reader) Bool() (bool, error) {
	v, err := r.Uvarint()
	if err != nil {
		return false, err
	}
	switch v {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("invalid bool value %d", v)
	}
}

// Bytes reads a length-prefixed byte slice (used for strings and memory
// ranges). The returned slice is always a freshly allocated copy.
func (r *reader) Bytes() ([]byte, error) {
	n, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	if r.limits.MaxMemRangeSize != 0 && n > r.limits.MaxMemRangeSize {
		return nil, fmt.Errorf("memory range length %d exceeds limit %d", n, r.limits.MaxMemRangeSize)
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if err := r.readFull(buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// String reads a length-prefixed UTF-8 string.
func (r *reader) String() (string, error) {
	n, err := r.Uvarint()
	if err != nil {
		return "", err
	}
	if r.limits.MaxStringBytes != 0 && n > r.limits.MaxStringBytes {
		return "", fmt.Errorf("string length %d exceeds limit %d", n, r.limits.MaxStringBytes)
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if err := r.readFull(buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	return string(buf), nil
}

// FieldList reads a heap dump field list terminated by FieldKindEol.
func (r *reader) FieldList() ([]heapsnapshot.Field, error) {
	var fields []heapsnapshot.Field
	for {
		kind, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		k := heapsnapshot.FieldKind(kind)
		if k == heapsnapshot.FieldKindEol {
			return fields, nil
		}
		off, err := r.Uvarint()
		if err != nil {
			return nil, err
		}
		fields = append(fields, heapsnapshot.Field{Kind: k, Offset: off})
	}
}

// readLine reads until the next '\n' (inclusive) and returns the line
// excluding the newline. It is used for the heap dump header which is
// not uvarint-encoded.
func (r *reader) readLine() (string, error) {
	line, err := r.r.ReadString('\n')
	r.offset += int64(len(line))
	if err != nil {
		if err == io.EOF && line == "" {
			return "", err
		}
		if err == io.EOF {
			return "", io.ErrUnexpectedEOF
		}
		return "", err
	}
	return line[:len(line)-1], nil
}
