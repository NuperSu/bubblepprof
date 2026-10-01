package bundle

import (
	"archive/tar"
	"bytes"
	"io"
	"math"
	"os"
	"strings"
	"testing"
)

type limitMember struct {
	name string
	data string
}

func limitsTar(t *testing.T, members []limitMember, advertised *tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Size: int64(len(m.data)), Mode: 0600}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, m.data); err != nil {
			t.Fatal(err)
		}
	}
	if advertised != nil {
		if err := tw.WriteHeader(advertised); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestOpenMemberLimitsBeforePayload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		opts  ReaderOptions
	}{
		{MetaMember, math.MaxInt64 - 1, ReaderOptions{}},
		{MetaMember, DefaultMaxMetadataBytes, ReaderOptions{}},
		{SegmentsMember, DefaultMaxSegmentIndexBytes, ReaderOptions{}},
		{"rodata/00000.bin", DefaultMaxRodataBytes, ReaderOptions{}},
		{MetaMember, 8, ReaderOptions{MaxMetadataBytes: 8}},
		{SegmentsMember, 8, ReaderOptions{MaxSegmentIndexBytes: 8}},
		{"rodata/00000.bin", 8, ReaderOptions{MaxRodataBytes: 8}},
	} {
		data := limitsTar(t, nil, &tar.Header{Name: tc.name, Size: tc.limit + 1})
		_, err := OpenWithOptions(bytes.NewReader(data), tc.opts)
		if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("name=%s err=%v", tc.name, err)
		}
	}
}

func TestOpenAggregateRodataAndOverrides(t *testing.T) {
	members := []limitMember{
		{MetaMember, `{"format_version":1}`}, {HeapDumpMember, "dump"},
		{SegmentsMember, `[]`}, {"rodata/00000.bin", "1234"}, {"rodata/00001.bin", "5678"},
	}
	data := limitsTar(t, members, nil)
	for _, cap := range []int64{7, 8, 9} {
		b, err := OpenWithOptions(bytes.NewReader(data), ReaderOptions{MaxRodataBytes: cap, MaxMetadataBytes: int64(len(members[0].data)), MaxSegmentIndexBytes: 2})
		if cap == 7 {
			if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
				t.Fatalf("cap=%d err=%v", cap, err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, opts := range []ReaderOptions{{MaxMetadataBytes: 1}, {MaxSegmentIndexBytes: 1}} {
		if _, err := OpenWithOptions(bytes.NewReader(data), opts); err == nil {
			t.Fatal("expected JSON size limit")
		}
	}
}

func TestOpenDuplicatesCleanDump(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	for _, name := range []string{MetaMember, SegmentsMember, HeapDumpMember, "rodata/00000.bin"} {
		payload := "x"
		if name == MetaMember {
			payload = `{"format_version":1}`
		}
		if name == SegmentsMember {
			payload = `[]`
		}
		members := []limitMember{{HeapDumpMember, "dump"}}
		if name != HeapDumpMember {
			members = append(members, limitMember{name, payload})
		}
		members = append(members, limitMember{name, payload})
		_, err := Open(bytes.NewReader(limitsTar(t, members, nil)))
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("name=%s err=%v", name, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("extracted dump leaked: %v", entries)
		}
	}
}

func TestOpenOversizeCleansDump(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := limitsTar(t, []limitMember{{HeapDumpMember, "dump"}}, &tar.Header{Name: MetaMember, Size: DefaultMaxMetadataBytes + 1})
	if _, err := Open(bytes.NewReader(data)); err == nil {
		t.Fatal("expected limit error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("extracted dump leaked: %v", entries)
	}
}

func TestOpenReaderLimitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		opts ReaderOptions
	}{
		{MetaMember, DefaultMaxMetadataBytes + 1, ReaderOptions{MaxMetadataBytes: DefaultMaxMetadataBytes + 1}},
		{SegmentsMember, DefaultMaxSegmentIndexBytes + 1, ReaderOptions{MaxSegmentIndexBytes: DefaultMaxSegmentIndexBytes + 1}},
		{"rodata/00000.bin", DefaultMaxRodataBytes + 1, ReaderOptions{MaxRodataBytes: DefaultMaxRodataBytes + 1}},
	} {
		data := limitsTar(t, nil, &tar.Header{Name: tc.name, Size: tc.size})
		_, err := OpenWithOptions(bytes.NewReader(data), tc.opts)
		if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
			t.Fatalf("name=%s err=%v", tc.name, err)
		}
	}
	for _, opts := range []ReaderOptions{{MaxRodataBytes: -1}, {MaxMetadataBytes: -1}, {MaxSegmentIndexBytes: -1}} {
		if _, err := OpenWithOptions(bytes.NewReader(nil), opts); err == nil {
			t.Fatal("negative limit accepted")
		}
	}
}
