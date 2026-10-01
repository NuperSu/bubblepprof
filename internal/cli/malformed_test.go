package cli

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/heapdump"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

func TestAnalyzerMalformedHeapMemoryLengths(t *testing.T) {
	for _, tc := range []struct {
		n          uint64
		flag, want string
	}{
		{math.MaxUint64, "18446744073709551615", "platform allocation bound"},
		{heapdump.DefaultMaxMemRangeBytes + 1, "0", "exceeds limit"},
		{heapdump.DefaultMaxMemRangeBytes + 1, "1073741825", "unexpected EOF"},
		{9, "8", "exceeds limit"},
		{9, "9", "unexpected EOF"},
	} {
		var dump bytes.Buffer
		dump.WriteString(heapdump.Header + "\n")
		var raw [10]byte
		for _, n := range []uint64{1, 0x8000, tc.n} {
			count := binary.PutUvarint(raw[:], n)
			dump.Write(raw[:count])
		}
		var artifact bytes.Buffer
		if err := bundle.Write(&artifact, bundle.WriteInput{HeapDump: bytes.NewReader(dump.Bytes()), HeapDumpSize: int64(dump.Len())}); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(t.TempDir(), "bad.tar")
		if err := os.WriteFile(name, artifact.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"memusage", "bubbles"} {
			args := []string{command, name, "-max-mem-range-bytes", tc.flag}
			if command == "memusage" {
				args = append(args, "-labels", "tenant=test")
			}
			var stdout, stderr bytes.Buffer
			if code := Main(args, &stdout, &stderr); code != exitFailure {
				t.Fatalf("command=%s code=%d stderr=%s", command, code, &stderr)
			}
			var response memusage.ErrorResponse
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("invalid response %s: %v", &stdout, err)
			}
			if response.Code != "parse_failed" || !strings.Contains(response.Error, tc.want) {
				t.Fatalf("command=%s response=%+v want %s", command, response, tc.want)
			}
		}
	}
}

func TestAnalyzerOversizedBundle(t *testing.T) {
	var artifact bytes.Buffer
	tw := tar.NewWriter(&artifact)
	if err := tw.WriteHeader(&tar.Header{Name: bundle.MetaMember, Size: bundle.DefaultMaxMetadataBytes + 1}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "bad.tar")
	if err := os.WriteFile(name, artifact.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"memusage", "bubbles"} {
		args := []string{command, name}
		if command == "memusage" {
			args = append(args, "-labels", "tenant=test")
		}
		var stdout, stderr bytes.Buffer
		if code := Main(args, &stdout, &stderr); code != exitFailure || !strings.Contains(stderr.String(), "exceeds limit") {
			t.Fatalf("command=%s code=%d stderr=%s", command, code, &stderr)
		}
	}
}
