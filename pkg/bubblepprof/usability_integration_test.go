package bubblepprof

import (
	"bytes"
	"encoding/json"
	"github.com/NuperSu/bubblepprof/internal/cli"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func TestCLIInspectAndAliases(t *testing.T) {
	if testing.Short() {
		t.Skip("live capture")
	}
	stop := startLabeledGoroutine(t, pprof.Labels("phase6", "worker"), 32<<10)
	defer stop()
	rr := httptest.NewRecorder()
	BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	path := filepath.Join(t.TempDir(), "capture.tar")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, err bytes.Buffer
		if code := cli.Main(args, &out, &err); code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, &err, &out)
		}
		return out.Bytes()
	}
	if !bytes.Equal(run("query", path, "--labels", "phase6=worker"), run("memusage", path, "--labels", "phase6=worker")) {
		t.Fatal("query alias changed result")
	}
	if !bytes.Equal(run("labels", path), run("bubbles", path)) {
		t.Fatal("labels alias changed result")
	}
	var result struct {
		Metadata struct {
			GoVersion string `json:"go_version"`
			Recovered int    `json:"recovered_goroutines"`
		} `json:"metadata"`
		LabelKeys []string `json:"label_keys"`
	}
	if err := json.Unmarshal(run("inspect", path, "--format", "json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Metadata.GoVersion == "" || result.Metadata.Recovered < 1 {
		t.Fatal(result)
	}
	if !strings.Contains(string(run("labels", path, "--format", "text")), "phase6") {
		t.Fatal("missing labels")
	}
	if !strings.Contains(string(run("inspect", path)), "validation:") {
		t.Fatal("missing metadata")
	}
}
func TestRegisterSharedCooldown(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWithOptions(mux, MemUsageOptions{CaptureCooldown: time.Hour}, BundleOptions{ScratchDir: filepath.Join(t.TempDir(), "missing")})
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if first.Code != 500 || !strings.Contains(first.Body.String(), "capture_failed") {
		t.Fatal(first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	mux.ServeHTTP(second, httptest.NewRequest(http.MethodPost, MemUsagePath, strings.NewReader(`{"labels":{"job":"test"}}`)))
	if second.Code != 429 || !strings.Contains(second.Body.String(), "cooldown") || second.Header().Get("Retry-After") == "" {
		t.Fatal(second.Code, second.Body.String())
	}
}
