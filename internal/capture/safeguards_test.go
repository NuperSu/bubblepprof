package capture

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestScratchPreflightLimitsAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int64
		spaceErr error
		wantErr  string
	}{{"ok", 0, nil, ""}, {"limit", 2, nil, "exceeds limit"}, {"negative", -1, nil, "non-negative"}, {"space", 0, errors.New("insufficient space"), "insufficient space"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts, _ := goodOpts()
			opts.ScratchDir = dir
			opts.MaxHeapDumpBytes = tc.limit
			called := false
			opts.CheckSpace = func(path string, min uint64) error {
				called = true
				if !strings.HasPrefix(path, dir) {
					t.Fatal(path)
				}
				return tc.spaceErr
			}
			dump, err := Capture(context.Background(), opts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				dump.Cleanup()
				dump.Cleanup()
			}
			if tc.limit >= 0 && !called {
				t.Fatal("no preflight")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("scratch leak", entries, err)
			}
		})
	}
}
