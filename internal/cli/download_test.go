package cli

import (
	"bytes"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func validDownloadBundle(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	dump := []byte("go1.7 heap dump\n\x00")
	if err := bundle.Write(&out, bundle.WriteInput{HeapDump: bytes.NewReader(dump), HeapDumpSize: int64(len(dump))}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestDownloadValidationAndSizeLimit(t *testing.T) {
	valid := validDownloadBundle(t)
	for _, tc := range []struct {
		name  string
		data  []byte
		limit int64
		ok    bool
	}{{"valid", valid, int64(len(valid)), true}, {"html", []byte("<html>error</html>"), 0, false}, {"truncated", valid[:len(valid)-512], 0, false}, {"limit", valid, int64(len(valid) - 1), false}, {"negative", valid, -1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f, err := stageDownload(dir, bytes.NewReader(tc.data), tc.limit)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				data, e := io.ReadAll(f)
				if e != nil || !bytes.Equal(data, valid) {
					t.Fatal(e)
				}
				f.Close()
				os.Remove(f.Name())
			} else if err == nil {
				f.Close()
				t.Fatal("accepted invalid download")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("spool leak", entries)
			}
		})
	}
}
func TestFetchInvalidArchivePreservesDestinationAndStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("invalid archive")) }))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "capture.tar")
	os.WriteFile(dest, []byte("old"), 0600)
	for _, output := range []string{dest, "-"} {
		var out, errs bytes.Buffer
		if code := Main([]string{"fetch", srv.URL, "-o", output}, &out, &errs); code != exitFailure {
			t.Fatal(code)
		}
		if out.Len() != 0 {
			t.Fatal("invalid bytes written to stdout")
		}
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "old" {
		t.Fatal("destination replaced")
	}
}
