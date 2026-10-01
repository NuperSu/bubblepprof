package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type failedDownload struct{}

func (failedDownload) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSaveDownload(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			dir := t.TempDir()
			name := filepath.Join(dir, "bundle.tar")
			if existing {
				if err := os.WriteFile(name, []byte("old"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			var src io.Reader = strings.NewReader("new")
			if fail {
				src = io.MultiReader(strings.NewReader("partial"), failedDownload{})
			}
			err := saveDownload(name, src)
			if fail && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error=%v", err)
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			wantFiles := 1
			if fail && !existing {
				wantFiles = 0
			}
			if len(entries) != wantFiles {
				t.Fatalf("temporary file leaked: %v", entries)
			}
			if wantFiles == 0 {
				continue
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			want := "new"
			if fail {
				want = "old"
			}
			if string(data) != want {
				t.Fatalf("data=%q want %q", data, want)
			}
			if !fail && runtime.GOOS != "windows" {
				info, err := os.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0600 {
					t.Fatalf("permissions=%o", info.Mode().Perm())
				}
			}
		}
	}
}

func TestFetchTruncatedResponsePreservesDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	name := filepath.Join(dir, "bundle.tar")
	if err := os.WriteFile(name, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runFetch([]string{srv.URL, "-o", name}, &out, &stderr); code != exitFailure {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(stderr.String(), "wrote ") {
		t.Fatalf("false success: %s", &stderr)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "old" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files=%v", entries)
	}
}

func TestSaveDownloadRenameFailureCleanup(t *testing.T) {
	dir := t.TempDir()
	if err := saveDownload(dir, strings.NewReader("new")); err == nil {
		t.Fatal("expected rename error")
	}
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bubblepprof-download-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}
