package snapshot

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReadSnapshotBundle(t *testing.T) {
	var buf bytes.Buffer
	metadata := testMetadata()
	if err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:         bytes.NewReader([]byte("FAKE_HEAP_DUMP")),
		HeapDumpSize:     int64(len("FAKE_HEAP_DUMP")),
		GoroutineProfile: []byte("FAKE_GOROUTINE_PROFILE"),
		Metadata:         metadata,
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	bundle, err := ReadSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if string(bundle.HeapDump) != "FAKE_HEAP_DUMP" {
		t.Fatalf("heap dump = %q", bundle.HeapDump)
	}
	if string(bundle.GoroutineProfile) != "FAKE_GOROUTINE_PROFILE" {
		t.Fatalf("goroutine profile = %q", bundle.GoroutineProfile)
	}
	if bundle.Metadata.Format != FormatV1 {
		t.Fatalf("format = %q", bundle.Metadata.Format)
	}
}

func TestReadSnapshotBundleErrors(t *testing.T) {
	metadataJSON, err := json.Marshal(testMetadata())
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	tests := []struct {
		name    string
		files   map[string][]byte
		wantErr string
	}{
		{
			name: "missing heap dump",
			files: map[string][]byte{
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         metadataJSON,
			},
			wantErr: "missing heap.dump",
		},
		{
			name: "missing goroutine profile",
			files: map[string][]byte{
				HeapDumpFile: []byte("heap"),
				MetadataFile: metadataJSON,
			},
			wantErr: "missing goroutine.pprof",
		},
		{
			name: "missing metadata",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
			},
			wantErr: "missing metadata.json",
		},
		{
			name: "invalid metadata",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         []byte("{"),
			},
			wantErr: "decode metadata.json",
		},
		{
			name: "wrong format",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         []byte(`{"format":"other"}`),
			},
			wantErr: "unsupported snapshot format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeTestTar(t, &buf, tt.files)

			_, err := ReadSnapshotBundle(bytes.NewReader(buf.Bytes()))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestInspectSnapshotBundle(t *testing.T) {
	var buf bytes.Buffer
	metadata := testMetadata()
	if err := WriteSnapshotBundle(&buf, BundleSource{
		HeapDump:         bytes.NewReader([]byte("FAKE_HEAP_DUMP")),
		HeapDumpSize:     int64(len("FAKE_HEAP_DUMP")),
		GoroutineProfile: []byte("FAKE_GOROUTINE_PROFILE"),
		Metadata:         metadata,
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	info, err := InspectSnapshotBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("inspect bundle: %v", err)
	}
	if info.HeapDumpSize != int64(len("FAKE_HEAP_DUMP")) {
		t.Fatalf("heap dump size = %d", info.HeapDumpSize)
	}
	if info.GoroutineProfileSize != int64(len("FAKE_GOROUTINE_PROFILE")) {
		t.Fatalf("goroutine profile size = %d", info.GoroutineProfileSize)
	}
	if info.Metadata.Format != FormatV1 {
		t.Fatalf("format = %q", info.Metadata.Format)
	}
}

func TestInspectSnapshotBundleErrors(t *testing.T) {
	metadataJSON, err := json.Marshal(testMetadata())
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	tests := []struct {
		name    string
		files   map[string][]byte
		wantErr string
	}{
		{
			name: "missing heap dump",
			files: map[string][]byte{
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         metadataJSON,
			},
			wantErr: "missing heap.dump",
		},
		{
			name: "missing goroutine profile",
			files: map[string][]byte{
				HeapDumpFile: []byte("heap"),
				MetadataFile: metadataJSON,
			},
			wantErr: "missing goroutine.pprof",
		},
		{
			name: "missing metadata",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
			},
			wantErr: "missing metadata.json",
		},
		{
			name: "invalid metadata",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         []byte("{"),
			},
			wantErr: "decode metadata.json",
		},
		{
			name: "wrong format",
			files: map[string][]byte{
				HeapDumpFile:         []byte("heap"),
				GoroutineProfileFile: []byte("profile"),
				MetadataFile:         []byte(`{"format":"other"}`),
			},
			wantErr: "unsupported snapshot format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeTestTar(t, &buf, tt.files)

			_, err := InspectSnapshotBundle(bytes.NewReader(buf.Bytes()))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func testMetadata() SnapshotMetadata {
	return SnapshotMetadata{
		Format:               FormatV1,
		CreatedAt:            time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		GoVersion:            "go-test",
		PID:                  12345,
		HeapDumpFile:         HeapDumpFile,
		GoroutineProfileFile: GoroutineProfileFile,
		GCBeforeHeapDump:     true,
	}
}

func writeTestTar(t *testing.T, buf *bytes.Buffer, files map[string][]byte) {
	t.Helper()

	tw := tar.NewWriter(buf)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o600,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write data: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}
