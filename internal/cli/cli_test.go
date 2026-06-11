package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // substring of stdout
		wantErr  string // substring of stderr
	}{
		{"no args", nil, exitUsage, "", "Usage:"},
		{"unknown command", []string{"frobnicate"}, exitUsage, "", `unknown command "frobnicate"`},
		{"help", []string{"help"}, exitOK, "Usage:", ""},
		{"memusage without labels", []string{"memusage", "x.tar"}, exitUsage, "", "-labels or -label is required"},
		{"memusage without arg", []string{"memusage", "-labels", "a=b"}, exitUsage, "", "exactly one bundle file"},
		{"fetch without arg", []string{"fetch"}, exitUsage, "", "exactly one target URL"},
		// The OS error text differs per platform ("no such file" vs "The
		// system cannot find the file specified"), so assert on the path.
		{"memusage missing file", []string{"memusage", "/nonexistent.tar", "-labels", "a=b"}, exitFailure, "", "/nonexistent.tar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			code := Main(tc.args, &out, &errBuf)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, tc.wantCode, errBuf.String())
			}
			if tc.wantOut != "" && !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("stdout %q missing %q", out.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(errBuf.String(), tc.wantErr) {
				t.Errorf("stderr %q missing %q", errBuf.String(), tc.wantErr)
			}
		})
	}
}

func TestLabelsFlag(t *testing.T) {
	f := labelsFlag{}
	if err := f.Set("job=42,tenant=acme"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Set("note=a,b"); err == nil {
		// "b" has no '=' so the whole Set must fail.
		t.Fatal("comma item without = must error")
	}
	if f["job"] != "42" || f["tenant"] != "acme" {
		t.Fatalf("labels = %v", f)
	}
	// Repeatable flag: a value containing '=' in the value part.
	if err := f.Set("expr=a=b"); err != nil {
		t.Fatalf("Set value with '=': %v", err)
	}
	if f["expr"] != "a=b" {
		t.Fatalf("expr = %q", f["expr"])
	}
	if err := f.Set("=v"); err == nil {
		t.Fatal("empty key must error")
	}
	if err := f.Set("novalue"); err == nil {
		t.Fatal("missing = must error")
	}

	exact := exactLabelFlag{labels: labelsFlag{}}
	if err := exact.Set("note=a,b"); err != nil {
		t.Fatalf("exact Set comma value: %v", err)
	}
	if exact.labels["note"] != "a,b" {
		t.Fatalf("note = %q, want comma-containing value", exact.labels["note"])
	}
	if err := exact.Set("novalue"); err == nil {
		t.Fatal("exact label missing = must error")
	}
}

func TestBundleURL(t *testing.T) {
	cases := []struct {
		in   string
		gc   bool
		want string
	}{
		{"http://host:6060", true, "http://host:6060/debug/memusage/bundle?gc=1"},
		{"http://host:6060/", false, "http://host:6060/debug/memusage/bundle?gc=0"},
		{"https://host/debug/memusage/bundle", true, "https://host/debug/memusage/bundle?gc=1"},
		{"https://host/debug/memusage/bundle/", true, "https://host/debug/memusage/bundle?gc=1"},
		{"http://host/app", true, "http://host/app/debug/memusage/bundle?gc=1"},
	}
	for _, tc := range cases {
		u, err := bundleURL(tc.in, tc.gc)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if u.String() != tc.want {
			t.Errorf("bundleURL(%q) = %q, want %q", tc.in, u.String(), tc.want)
		}
	}
	if _, err := bundleURL("ftp://host", true); err == nil {
		t.Error("non-http scheme must error")
	}
	if _, err := bundleURL("http://host \x7f", true); err == nil {
		t.Error("unparsable URL must error")
	}
}

// TestFetchEndToEnd drives the fetch subcommand against a stub server
// (the bundle bytes are opaque to fetch, so no real capture is needed).
func TestFetchEndToEnd(t *testing.T) {
	payload := []byte("not-really-a-tar-but-fetch-does-not-care")
	var gotPath, gotGC string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGC = r.URL.Query().Get("gc")
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.tar")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"fetch", srv.URL, "-o", out, "-gc=false"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
	}
	if gotPath != "/debug/memusage/bundle" || gotGC != "0" {
		t.Fatalf("request = %s?gc=%s, want /debug/memusage/bundle?gc=0", gotPath, gotGC)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output = %q, want server payload", got)
	}

	// "-o -" streams the bundle bytes to stdout.
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"fetch", srv.URL, "-o", "-"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("stdout mode exit code = %d, stderr: %s", code, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), payload) {
		t.Fatalf("stdout = %q, want server payload", stdout.Bytes())
	}
}

// TestFetchNon200 verifies a non-200 target response surfaces as a
// transport failure, not a saved error page.
func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"busy","code":"busy"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.tar")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"fetch", srv.URL, "-o", out}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), "429") {
		t.Fatalf("stderr %q should mention the HTTP status", stderr.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("no output file should be created on failure: %v", err)
	}
}
