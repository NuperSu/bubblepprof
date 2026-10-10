package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/cli"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
	"github.com/google/pprof/profile"
)

func TestBundleExportPprof(t *testing.T) {
	if testing.Short() {
		t.Skip("live capture and go tool pprof")
	}
	rate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = rate }()
	stop := startLabeledGoroutine(t, pprof.Labels("phase5", "export"), 64<<10)
	defer stop()
	runtime.MemProfileRate = rate
	rr := httptest.NewRecorder()
	BundleHandlerWithOptions(BundleOptions{IncludeHeapProfile: true, IncludeGoroutineProfile: true}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.tar")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := offline.OpenSnapshot(context.Background(), path, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want, err := s.Export(context.Background(), memusage.Request{Labels: map[string]string{"phase5": "export"}})
	if err != nil {
		t.Fatal(err)
	}
	objects, e := s.Objects(context.Background(), memusage.Request{Labels: map[string]string{"phase5": "export"}}, memusage.ObjectOptions{})
	if e != nil {
		t.Fatal(e)
	}
	sampledPayload := false
	for _, o := range objects.Objects {
		if o.Size == 64<<10 {
			for _, sample := range o.AllocationSamples {
				for _, frame := range sample.Profile.Frames {
					if strings.Contains(frame.Function, "startLabeledGoroutine") {
						sampledPayload = true
					}
				}
			}
		}
	}
	if !sampledPayload {
		t.Fatal("runtime sampled payload allocation origin was not retained")
	}
	output := filepath.Join(dir, "reachability.pb.gz")
	var stdout, stderr bytes.Buffer
	if code := cli.Main([]string{"export", path, "--labels", "phase5=export", "--format", "pprof", "-o", output}, &stdout, &stderr); code != 0 {
		t.Fatalf("%d %s", code, &stderr)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.ParseData(data)
	if err != nil {
		t.Fatal(err)
	}
	totals := make([]int64, 6)
	for _, sample := range p.Sample {
		for i, v := range sample.Value {
			totals[i] += v
		}
		if !reflect.DeepEqual(sample.Label["phase5"], []string{"export"}) {
			t.Fatal(sample.Label)
		}
	}
	m := want.Summary.Metrics
	if !reflect.DeepEqual(totals, []int64{int64(m["reachable"].Objects), int64(m["reachable"].Bytes), int64(m["exclusive"].Objects), int64(m["exclusive"].Bytes), int64(m["shared"].Objects), int64(m["shared"].Bytes)}) {
		t.Fatal(totals, m)
	}
	pathsOutput := filepath.Join(dir, "paths.pb.gz")
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main([]string{"export", path, "--labels", "phase5=export", "--pprof-mode", "paths", "-o", pathsOutput}, &stdout, &stderr); code != 0 {
		t.Fatalf("paths export: %d %s", code, &stderr)
	}
	pathsData, err := os.ReadFile(pathsOutput)
	if err != nil {
		t.Fatal(err)
	}
	pathsProfile, err := profile.ParseData(pathsData)
	if err != nil {
		t.Fatal(err)
	}
	pathsTotals := make([]int64, 6)
	addresses := map[string]bool{}
	for _, sample := range pathsProfile.Sample {
		for i, v := range sample.Value {
			pathsTotals[i] += v
		}
		address := sample.Label["bubblepprof.object_address"][0]
		if addresses[address] || len(sample.Location) < 5 || len(sample.Location) > memusage.DefaultPprofPathDepth+5 || !strings.Contains(sample.Location[0].Line[0].Function.Name, "object "+address+" shallow=") {
			t.Fatal("invalid object path", sample)
		}
		addresses[address] = true
		for _, loc := range sample.Location {
			if loc.Address != 0 || !strings.HasPrefix(loc.Line[0].Function.Name, "[bubblepprof reference]") {
				t.Fatal("synthetic path disguised as code", loc)
			}
		}
	}
	if !reflect.DeepEqual(pathsTotals, totals) || len(pathsProfile.Sample) != m["reachable"].Objects || pathsProfile.TimeNanos != p.TimeNanos {
		t.Fatal("paths/summary mismatch", pathsTotals, totals)
	}
	for _, object := range objects.Objects {
		if !addresses[object.Address] {
			t.Fatal("missing reachable object", object)
		}
	}
	t.Run("pprof without a host C compiler", func(t *testing.T) {
		// Reproduce a cross-build environment whose CC cannot build host tools.
		t.Setenv("CC", filepath.Join(t.TempDir(), "missing-cross-compiler"))
		t.Setenv("CGO_ENABLED", "1")
		for _, tc := range []struct{ output, prefix string }{{output, "bubblepprof summary"}, {pathsOutput, "bubblepprof reference"}} {
			for _, index := range []string{"reachable_space", "exclusive_space", "shared_space"} {
				cmd := exec.Command("go", "tool", "pprof", "-top", "-sample_index="+index, tc.output)
				// Go 1.25+ may build pprof for the host on demand, while CC still
				// targets the test architecture (e.g. ARM under QEMU). This report
				// only needs pure Go pprof; leave cgo enabled for the outer PIE tests.
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("go tool pprof: %v %s", err, out)
				}
				if !strings.Contains(string(out), tc.prefix) {
					t.Fatalf("missing synthetic frames: %s", out)
				}
			}
		}
	})
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main([]string{"export", path, "--label", "phase5=export", "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("%d %s", code, &stderr)
	}
	var got struct{ memusage.ExportReport }
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || !reflect.DeepEqual(got.Summary, want.Summary) {
		t.Fatalf("%+v", got)
	}
	// Optional destination enables the separately invoked real-backend smoke test.
	if fixtureDir := os.Getenv("BUBBLEPPROF_EXPORT_FIXTURE_DIR"); fixtureDir != "" {
		if err := os.WriteFile(filepath.Join(fixtureDir, "reachability.pb.gz"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixtureDir, "report.json"), stdout.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
