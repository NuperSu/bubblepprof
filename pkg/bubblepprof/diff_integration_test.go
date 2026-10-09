package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/cli"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

func TestBundleSnapshotDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("live heap dump")
	}
	stop := startLabeledGoroutine(t, pprof.Labels("phase4", "before"), 32<<10)
	capture := func(name string) string {
		rr := httptest.NewRecorder()
		BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
		if rr.Code != 200 {
			t.Fatal(rr.Body.String())
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	beforePath := capture("before.tar")
	stop()
	stopAfter := startLabeledGoroutine(t, pprof.Labels("phase4", "after"), 64<<10)
	defer stopAfter()
	afterPath := capture("after.tar")
	ctx := context.Background()
	before, err := offline.OpenSnapshot(ctx, beforePath, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	after, err := offline.OpenSnapshot(ctx, afterPath, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	r, err := offline.Diff(ctx, before, after, offline.DiffOptions{By: "phase4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != 2 {
		t.Fatalf("%+v", r.Groups)
	}
	for _, g := range r.Groups {
		if g.Labels["phase4"] == "before" && (!g.Before.Present || g.After.Present) {
			t.Fatalf("%+v", g)
		}
		if g.Labels["phase4"] == "after" && (g.Before.Present || !g.After.Present) {
			t.Fatalf("%+v", g)
		}
	}
	if before.Capture.ApplicationBuild == nil || before.Capture.CaptureSettings == nil {
		t.Fatal("missing new capture metadata")
	}
	for _, args := range [][]string{{"--by", "phase4"}, {"--labels", "phase4=before"}, {"--a", "phase4=before", "--b", "phase4=after"}} {
		var out, errs bytes.Buffer
		command := append([]string{"diff", beforePath, afterPath, "--format", "json"}, args...)
		if code := cli.Main(command, &out, &errs); code != 0 {
			t.Fatalf("%d %s %s", code, &errs, &out)
		}
		var got offline.DiffResult
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.SchemaVersion != 1 || len(got.Compatibility) < 6 {
			t.Fatalf("%+v", got)
		}
		if len(args) > 2 && got.Pair == nil {
			t.Fatal("missing pair")
		}
	}
	// Older bundles retain usable comparisons but explicitly unknown metadata.
	before.Capture.ApplicationBuild = nil
	before.Capture.CaptureSettings = nil
	legacy, err := offline.Diff(ctx, before, after, offline.DiffOptions{Labels: map[string]string{"phase4": "before"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range legacy.Compatibility {
		if (c.Field == "application_build" || c.Field == "capture_settings") && c.Status != "unknown" {
			t.Fatal(c)
		}
	}
	var text, errs bytes.Buffer
	if code := cli.Main([]string{"diff", beforePath, "--by", "phase4", afterPath}, &text, &errs); code != 0 {
		t.Fatalf("%d %s", code, &errs)
	}
	// Identical snapshots compare aggregates to zero, regardless of addresses.
	same, err := offline.Diff(ctx, before, before, offline.DiffOptions{Labels: map[string]string{"phase4": "before"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range same.Groups[0].Metrics {
		if d.Bytes != "0" || d.Objects != 0 {
			t.Fatal(d)
		}
	}
}
