package main

import "testing"

func TestTableVersionPrefix(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"go1.26.9", "go1.26."},
		{"go1.27rc2", "go1.27"},
		{"go1.28-devel_e203fe5746 Fri Oct 9 13:34:05 2026 -0700", "go1.28-devel_e203fe5746"},
		{"go1.28-devel", "go1.28-devel"},
	} {
		if got := tableVersionPrefix(tc.version); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.version, got, tc.want)
		}
	}
}
