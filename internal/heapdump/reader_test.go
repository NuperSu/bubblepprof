package heapdump

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"bubblepprof/internal/heapsnapshot"
)

func TestReaderUvarint(t *testing.T) {
	tests := []struct {
		name    string
		bytes   []byte
		want    uint64
		wantErr bool
	}{
		{name: "zero", bytes: []byte{0x00}, want: 0},
		{name: "small", bytes: []byte{0x7f}, want: 0x7f},
		{name: "two bytes", bytes: []byte{0x80, 0x01}, want: 0x80},
		{name: "max safe", bytes: encodeUvarint(1 << 50)},
		{name: "truncated", bytes: []byte{0x80}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newReader(bytes.NewReader(tc.bytes), Limits{})
			got, err := r.Uvarint()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.name == "max safe" {
				if got != 1<<50 {
					t.Fatalf("got %d, want %d", got, uint64(1<<50))
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReaderBool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bytes   []byte
		want    bool
		wantErr bool
	}{
		{name: "false", bytes: []byte{0}, want: false},
		{name: "true", bytes: []byte{1}, want: true},
		{name: "invalid", bytes: []byte{2}, wantErr: true},
		{name: "truncated", bytes: nil, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReader(bytes.NewReader(tc.bytes), Limits{})
			got, err := r.Bool()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %t, want %t", got, tc.want)
			}
		})
	}
}

func TestReaderString(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var buf bytes.Buffer
		writeString(&buf, "hello")
		r := newReader(&buf, Limits{})
		got, err := r.String()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hello" {
			t.Fatalf("got %q want %q", got, "hello")
		}
	})

	t.Run("truncated payload", func(t *testing.T) {
		buf := encodeUvarint(5)
		buf = append(buf, []byte("ab")...)
		r := newReader(bytes.NewReader(buf), Limits{})
		_, err := r.String()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("limit", func(t *testing.T) {
		buf := encodeUvarint(1000)
		r := newReader(bytes.NewReader(buf), Limits{MaxStringBytes: 8})
		_, err := r.String()
		if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("got err=%v, want exceeds limit", err)
		}
	})
}

func TestReaderFieldList(t *testing.T) {
	var buf bytes.Buffer
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindPtr))
	writeUvarint(&buf, 8)
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindEface))
	writeUvarint(&buf, 24)
	writeUvarint(&buf, uint64(heapsnapshot.FieldKindEol))

	r := newReader(&buf, Limits{})
	got, err := r.FieldList()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []heapsnapshot.Field{
		{Kind: heapsnapshot.FieldKindPtr, Offset: 8},
		{Kind: heapsnapshot.FieldKindEface, Offset: 24},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("field %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReaderLine(t *testing.T) {
	r := newReader(strings.NewReader("hello\nrest"), Limits{})
	line, err := r.readLine()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "hello" {
		t.Fatalf("got %q, want %q", line, "hello")
	}
}

func encodeUvarint(x uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], x)
	out := make([]byte, n)
	copy(out, buf[:n])
	return out
}
