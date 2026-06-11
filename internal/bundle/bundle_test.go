package bundle

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func writeTestBundle(t *testing.T, in WriteInput) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func openTestBundle(t *testing.T, raw []byte) *Bundle {
	t.Helper()
	b, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestWriteOpenRoundTrip(t *testing.T) {
	dump := []byte("fake heap dump bytes")
	seg0 := []byte("rodata segment zero")
	seg1 := []byte("rodata-1")
	raw := writeTestBundle(t, WriteInput{
		Meta: Meta{
			Producer:  "bubblepprof/test",
			GoVersion: "go1.99.0",
			GOARCH:    "amd64",
			PtrSize:   8,
			Rodata:    RodataMeta{Status: RodataOK},
		},
		Segments: []Segment{
			{Addr: 0x1000, Size: uint64(len(seg0)), Perms: "r--", Path: "/bin/x", R: bytes.NewReader(seg0)},
			{Addr: 0x9000, Size: uint64(len(seg1)), Perms: "r-x", R: bytes.NewReader(seg1)},
		},
		HeapDump:     bytes.NewReader(dump),
		HeapDumpSize: int64(len(dump)),
	})

	b := openTestBundle(t, raw)
	if b.Meta.FormatVersion != FormatVersion {
		t.Errorf("FormatVersion = %d, want %d", b.Meta.FormatVersion, FormatVersion)
	}
	if b.Meta.GoVersion != "go1.99.0" || b.Meta.Rodata.Segments != 2 {
		t.Errorf("meta round-trip mismatch: %+v", b.Meta)
	}
	if want := uint64(len(seg0) + len(seg1)); b.Meta.Rodata.TotalBytes != want {
		t.Errorf("TotalBytes = %d, want %d", b.Meta.Rodata.TotalBytes, want)
	}
	if len(b.Warnings) != 0 {
		t.Errorf("unexpected warnings for ok rodata: %v", b.Warnings)
	}

	got, err := os.ReadFile(b.HeapDumpPath)
	if err != nil {
		t.Fatalf("read extracted dump: %v", err)
	}
	if !bytes.Equal(got, dump) {
		t.Errorf("heap dump round-trip mismatch: %q", got)
	}

	if b.Segments == nil {
		t.Fatal("Segments is nil")
	}
	if data, ok := b.Segments.ReadAtAddr(0x1000, uint64(len(seg0))); !ok || !bytes.Equal(data, seg0) {
		t.Errorf("segment 0 read = %q ok=%t", data, ok)
	}
	if data, ok := b.Segments.ReadAtAddr(0x9000+2, 4); !ok || string(data) != "data" {
		t.Errorf("segment 1 interior read = %q ok=%t", data, ok)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(b.HeapDumpPath); !os.IsNotExist(err) && b.HeapDumpPath != "" {
		t.Errorf("temp dump not removed: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestOpenRejectsNewerFormatVersion(t *testing.T) {
	raw := writeTestBundle(t, WriteInput{
		HeapDump:     strings.NewReader("d"),
		HeapDumpSize: 1,
	})
	// Rewrite meta.json with a bumped version.
	var out bytes.Buffer
	tr := tar.NewReader(bytes.NewReader(raw))
	tw := tar.NewWriter(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == MetaMember {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			m["format_version"] = FormatVersion + 1
			data, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			hdr.Size = int64(len(data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Open(bytes.NewReader(out.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "newer than this bubblepprof") {
		t.Fatalf("Open with newer version: err = %v", err)
	}
}

func TestOpenMissingMembers(t *testing.T) {
	t.Run("missing heap.dump", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		meta, _ := json.Marshal(Meta{FormatVersion: FormatVersion, Rodata: RodataMeta{Status: RodataOK}})
		if err := tw.WriteHeader(&tar.Header{Name: MetaMember, Size: int64(len(meta))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(meta); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		if _, err := Open(&buf); err == nil || !strings.Contains(err.Error(), HeapDumpMember) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing meta.json", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: HeapDumpMember, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		if _, err := Open(&buf); err == nil || !strings.Contains(err.Error(), MetaMember) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenRodataDegradation(t *testing.T) {
	cases := []struct {
		name        string
		rodata      RodataMeta
		wantWarning string
	}{
		{
			name:        "unavailable",
			rodata:      RodataMeta{Status: RodataUnavailable, Reason: "no procfs"},
			wantWarning: "process memory reader unavailable: no procfs; literal pprof label strings may be unrecoverable",
		},
		{
			name:        "disabled",
			rodata:      RodataMeta{Status: RodataDisabled, Reason: "disabled by options"},
			wantWarning: "process memory reader disabled by options; literal pprof label strings may be unrecoverable",
		},
		{
			name:        "truncated",
			rodata:      RodataMeta{Status: RodataTruncated, Reason: "segment snapshot exceeds size cap"},
			wantWarning: "rodata snapshot truncated: segment snapshot exceeds size cap; literal pprof label strings may be unrecoverable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := writeTestBundle(t, WriteInput{
				Meta:         Meta{Rodata: tc.rodata},
				HeapDump:     strings.NewReader("d"),
				HeapDumpSize: 1,
			})
			b := openTestBundle(t, raw)
			if b.Segments != nil {
				t.Errorf("Segments = %v, want nil", b.Segments)
			}
			found := false
			for _, w := range b.Warnings {
				if w == tc.wantWarning {
					found = true
				}
			}
			if !found {
				t.Errorf("warnings %v missing %q", b.Warnings, tc.wantWarning)
			}
		})
	}
}

func TestOpenIgnoresUnknownMembers(t *testing.T) {
	raw := writeTestBundle(t, WriteInput{
		Meta:         Meta{Rodata: RodataMeta{Status: RodataOK}},
		HeapDump:     strings.NewReader("dump"),
		HeapDumpSize: 4,
	})
	// Append an unknown member by rebuilding the tar.
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(data)
	}
	extra := []byte("future extension")
	_ = tw.WriteHeader(&tar.Header{Name: "future/extra.bin", Size: int64(len(extra))})
	_, _ = tw.Write(extra)
	_ = tw.Close()

	b := openTestBundle(t, out.Bytes())
	if got, _ := os.ReadFile(b.HeapDumpPath); string(got) != "dump" {
		t.Errorf("heap dump = %q", got)
	}
}

func TestSegmentsReaderContract(t *testing.T) {
	infos := []SegmentInfo{
		{Member: "rodata/00000.bin", Addr: 0x1000, Size: 16},
		{Member: "rodata/00001.bin", Addr: 0x2000, Size: 8},
	}
	data := [][]byte{
		[]byte("0123456789abcdef"),
		[]byte("ABCDEFGH"),
	}
	r, err := NewSegmentsReader(infos, data)
	if err != nil {
		t.Fatalf("NewSegmentsReader: %v", err)
	}

	cases := []struct {
		name string
		addr uint64
		size uint64
		want string
		ok   bool
	}{
		{"zero size", 0xdead, 0, "", true},
		{"zero addr", 0, 8, "", false},
		{"overflow", ^uint64(0), 8, "", false},
		{"exact fit segment 0", 0x1000, 16, "0123456789abcdef", true},
		{"interior", 0x1004, 4, "4567", true},
		{"end of segment 1", 0x2006, 2, "GH", true},
		{"straddles gap", 0x1008, 16, "", false},
		{"past end", 0x2007, 2, "", false},
		{"before first", 0xfff, 4, "", false},
		{"between segments", 0x1800, 4, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := r.ReadAtAddr(tc.addr, tc.size)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if ok && tc.size > 0 && string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	if r.Name() != "bundle-rodata" {
		t.Errorf("Name() = %q", r.Name())
	}
	var nilReader *SegmentsReader
	if _, ok := nilReader.ReadAtAddr(0x1000, 1); ok {
		t.Error("nil receiver read must fail")
	}
}

func TestNewSegmentsReaderValidation(t *testing.T) {
	if _, err := NewSegmentsReader([]SegmentInfo{{Size: 4}}, nil); err == nil {
		t.Error("mismatched lengths must error")
	}
	if _, err := NewSegmentsReader([]SegmentInfo{{Size: 4}}, [][]byte{{1, 2}}); err == nil {
		t.Error("size mismatch must error")
	}
	if _, err := NewSegmentsReader([]SegmentInfo{{Addr: HexUint64(^uint64(0)), Size: 4}}, [][]byte{{1, 2, 3, 4}}); err == nil {
		t.Error("overflowing range must error")
	}
}

func TestHexUint64JSON(t *testing.T) {
	out, err := json.Marshal(HexUint64(0x55a0a4c00000))
	if err != nil || string(out) != `"0x55a0a4c00000"` {
		t.Fatalf("marshal = %s, %v", out, err)
	}
	var h HexUint64
	if err := json.Unmarshal([]byte(`"0xff"`), &h); err != nil || h != 0xff {
		t.Fatalf("unmarshal = %v, %v", h, err)
	}
	if err := json.Unmarshal([]byte(`"ff"`), &h); err == nil {
		t.Fatal("missing 0x prefix must error")
	}
	if err := json.Unmarshal([]byte(`123`), &h); err == nil {
		t.Fatal("non-string must error")
	}
}
