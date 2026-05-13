package snapshotparse

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/snapshot"
)

func TestParseSnapshotMissingMetadata(t *testing.T) {
	heapDump := buildHeapDump(t)
	buf := snapshotTar(t, map[string][]byte{
		snapshot.HeapDumpFile:         heapDump,
		snapshot.GoroutineProfileFile: []byte("profile"),
	})

	_, err := ParseSnapshot(bytes.NewReader(buf), heapdump.Options{})
	if err == nil || !strings.Contains(err.Error(), "snapshot missing metadata.json") {
		t.Fatalf("err = %v, want missing metadata", err)
	}
}

func TestParseSnapshotMissingGoroutineProfile(t *testing.T) {
	heapDump := buildHeapDump(t)
	metadata := metadataJSON(t, snapshot.FormatV1)
	buf := snapshotTar(t, map[string][]byte{
		snapshot.HeapDumpFile: heapDump,
		snapshot.MetadataFile: metadata,
	})

	_, err := ParseSnapshot(bytes.NewReader(buf), heapdump.Options{})
	if err == nil || !strings.Contains(err.Error(), "snapshot missing goroutine.pprof") {
		t.Fatalf("err = %v, want missing profile", err)
	}
}

func TestParseSnapshotInvalidMetadataJSON(t *testing.T) {
	heapDump := buildHeapDump(t)
	buf := snapshotTar(t, map[string][]byte{
		snapshot.HeapDumpFile:         heapDump,
		snapshot.GoroutineProfileFile: []byte("profile"),
		snapshot.MetadataFile:         []byte("{not json"),
	})

	_, err := ParseSnapshot(bytes.NewReader(buf), heapdump.Options{})
	if err == nil || !strings.Contains(err.Error(), "decode metadata.json") {
		t.Fatalf("err = %v, want metadata decode error", err)
	}
}

func TestParseSnapshotInvalidTar(t *testing.T) {
	_, err := ParseSnapshot(strings.NewReader("not a tar stream"), heapdump.Options{})
	if err == nil || !strings.Contains(err.Error(), "read tar") {
		t.Fatalf("err = %v, want tar read error", err)
	}
}

func snapshotTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, contents := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))}); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(contents); err != nil {
			t.Fatalf("write tar contents %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func metadataJSON(t *testing.T, format string) []byte {
	t.Helper()
	b, err := json.Marshal(snapshot.SnapshotMetadata{
		Format:               format,
		HeapDumpFile:         snapshot.HeapDumpFile,
		GoroutineProfileFile: snapshot.GoroutineProfileFile,
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return b
}
