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
	stop := startLabeledGoroutine(t, pprof.Labels("phase5", "export"), 64<<10)
	defer stop()
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
	for _, index := range []string{"reachable_space", "exclusive_space", "shared_space"} {
		cmd := exec.Command("go", "tool", "pprof", "-top", "-sample_index="+index, output)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go tool pprof: %v %s", err, out)
		}
		if !strings.Contains(string(out), "bubblepprof summary") {
			t.Fatalf("missing synthetic frames: %s", out)
		}
	}
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
