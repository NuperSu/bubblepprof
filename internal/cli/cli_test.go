package cli

import (
	"bytes"
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
		{"memusage without labels", []string{"memusage", "x.tar"}, exitUsage, "", "-labels is required"},
		{"memusage without arg", []string{"memusage", "-labels", "a=b"}, exitUsage, "", "exactly one bundle file"},
		{"fetch without arg", []string{"fetch"}, exitUsage, "", "exactly one target URL"},
		{"memusage missing file", []string{"memusage", "/nonexistent.tar", "-labels", "a=b"}, exitFailure, "", "no such file"},
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
