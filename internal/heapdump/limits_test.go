package heapdump

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestReaderPlatformLengths(t *testing.T) {
	for _, n := range []uint64{math.MaxUint64, uint64(^uint(0)>>1) + 1} {
		for _, str := range []bool{false, true} {
			r := newReader(bytes.NewReader(encodeUvarint(n)), Limits{})
			var err error
			if str {
				_, err = r.String()
			} else {
				_, err = r.Bytes()
			}
			if err == nil || !strings.Contains(err.Error(), "platform allocation bound") {
				t.Fatalf("n=%d string=%v err=%v", n, str, err)
			}
		}
	}
}

type boundedRead struct {
	src     io.Reader
	largest int
}

func (r *boundedRead) Read(p []byte) (int, error) {
	if len(p) > r.largest {
		r.largest = len(p)
	}
	return r.src.Read(p)
}

func TestReaderTruncatedLargePayloadUsesBoundedReads(t *testing.T) {
	for _, str := range []bool{false, true} {
		src := &boundedRead{src: bytes.NewReader(encodeUvarint(1 << 30))}
		r := newReader(src, Limits{})
		var err error
		if str {
			_, err = r.String()
		} else {
			_, err = r.Bytes()
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err=%v", err)
		}
		if src.largest > 64<<10 {
			t.Fatalf("read allocated %d bytes", src.largest)
		}
	}
}

func TestReaderChunkBoundary(t *testing.T) {
	for _, size := range []int{0, 1, 65535, 65536, 65537, 131073} {
		payload := bytes.Repeat([]byte{42}, size)
		var encoded bytes.Buffer
		writeBytes(&encoded, payload)
		r := newReader(&encoded, Limits{MaxMemRangeSize: uint64(size)})
		got, err := r.Bytes()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("size=%d err=%v", size, err)
		}
	}
}

func TestParserMemoryDefaultAndOverride(t *testing.T) {
	build := func(n uint64) *bytes.Buffer {
		buf := newSyntheticBuffer()
		writeUvarint(buf, tagObject)
		writeUvarint(buf, 0x8000)
		writeUvarint(buf, n)
		return buf
	}
	for _, tc := range []struct {
		n, limit uint64
		want     string
	}{
		{DefaultMaxMemRangeBytes + 1, 0, "exceeds limit"},
		{DefaultMaxMemRangeBytes, 0, "unexpected EOF"},
		{DefaultMaxMemRangeBytes + 1, DefaultMaxMemRangeBytes + 1, "unexpected EOF"},
		{9, 8, "exceeds limit"},
		{8, 8, "unexpected EOF"},
	} {
		_, err := Parse(build(tc.n), Options{MaxMemRangeBytes: tc.limit})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v err=%v", tc, err)
		}
	}
}
