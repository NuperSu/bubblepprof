package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/cli"
	"github.com/NuperSu/bubblepprof/internal/memusage"
)

// startLabeledGoroutine launches a goroutine carrying the given pprof
// labels that pins payloadBytes of heap until the returned stop func is
// called.
func startLabeledGoroutine(t *testing.T, labels pprof.LabelSet, payloadBytes int) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	pprof.Do(ctx, labels, func(ctx context.Context) {
		go func() {
			defer wg.Done()
			pprof.SetGoroutineLabels(ctx)
			data := make([]byte, payloadBytes)
			for i := 0; i < len(data); i += 4096 {
				data[i] = byte(i)
			}
			close(started)
			<-ctx.Done()
			runtime.KeepAlive(data)
		}()
	})
	<-started
	return func() {
		cancel()
		wg.Wait()
	}
}

// captureBundle fetches a bundle via BundleHandler and opens it.
func captureBundle(t *testing.T) *bundle.Bundle {
	t.Helper()
	rr := httptest.NewRecorder()
	BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("bundle capture status %d: %s", rr.Code, rr.Body.String())
	}
	b, err := bundle.Open(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("bundle.Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// analyzeOpenedBundle runs the offline analysis pipeline exactly as the
// CLI does.
func analyzeOpenedBundle(t *testing.T, b *bundle.Bundle, labels map[string]string) (*memusage.Response, error) {
	t.Helper()
	f, err := os.Open(b.HeapDumpPath)
	if err != nil {
		t.Fatalf("open extracted dump: %v", err)
	}
	defer f.Close()
	var extra addrspace.Reader
	if b.Segments != nil {
		extra = b.Segments
	}
	return memusage.AnalyzeDump(context.Background(), f, f, extra, b.Warnings,
		memusage.Request{Labels: labels}, memusage.Options{})
}

// TestBundle_HeapResidentLabels analyses a bundle of the test process
// and finds a goroutine labeled with heap-allocated label strings —
// the external-analyser equivalent of
// TestMemUsageHandler_RuntimePprofLabels.
func TestBundle_HeapResidentLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}
	key := strings.Clone("job")
	value := strings.Clone("bundle-heap")
	stop := startLabeledGoroutine(t, pprof.Labels(key, value), 8<<20)
	defer stop()

	b := captureBundle(t)
	resp, err := analyzeOpenedBundle(t, b, map[string]string{key: value})
	if err != nil {
		var sm *memusage.StringMissingError
		if errors.As(err, &sm) {
			t.Fatalf("heap-resident labels must not require string recovery: %v (warnings %v)", err, sm.Warnings)
		}
		t.Fatalf("AnalyzeDump: %v", err)
	}
	if resp.MatchedGoroutines < 1 {
		t.Fatalf("matched_goroutines = %d, want >= 1; resp = %+v", resp.MatchedGoroutines, resp)
	}
	if resp.ReachableBytes < 8<<20 {
		t.Fatalf("reachable_bytes = %d, want >= pinned payload (8 MiB)", resp.ReachableBytes)
	}
}

// TestBundle_LiteralLabels exercises rodata-segment string recovery:
// literal pprof.Labels strings live outside heap object contents and
// must be served by the bundle's saved read-only segments. When the
// platform cannot snapshot rodata, the analysis must fail with exactly
// string_missing — never a silent empty match.
func TestBundle_LiteralLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}
	stop := startLabeledGoroutine(t, pprof.Labels("job", "bundle-literal"), 4<<20)
	defer stop()

	b := captureBundle(t)
	resp, err := analyzeOpenedBundle(t, b, map[string]string{"job": "bundle-literal"})
	if err != nil {
		var sm *memusage.StringMissingError
		if errors.As(err, &sm) {
			if b.Segments != nil {
				t.Fatalf("string_missing despite rodata segments in bundle: %v (warnings %v)", err, sm.Warnings)
			}
			t.Skipf("rodata snapshot unavailable on this platform (status %q); honest string_missing", b.Meta.Rodata.Status)
		}
		t.Fatalf("AnalyzeDump: %v", err)
	}
	if resp.MatchedGoroutines < 1 {
		t.Fatalf("matched_goroutines = %d, want >= 1; resp = %+v", resp.MatchedGoroutines, resp)
	}
	if resp.ReachableBytes == 0 {
		t.Fatalf("reachable_bytes = 0; resp = %+v", resp)
	}
}

// TestBundle_CLIEndToEnd drives the real CLI entrypoint against an
// httptest server registered via Register, the way a user would run
// `bubblepprof memusage http://host:port -labels job=...`.
func TestBundle_CLIEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}
	key := strings.Clone("job")
	value := strings.Clone("cli-e2e")
	stop := startLabeledGoroutine(t, pprof.Labels(key, value), 8<<20)
	defer stop()

	mux := http.NewServeMux()
	Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errBuf bytes.Buffer
	code := cli.Main([]string{"memusage", srv.URL, "-labels", key + "=" + value}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("cli exit code = %d\nstdout: %s\nstderr: %s", code, out.String(), errBuf.String())
	}
	var resp memusage.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode CLI output: %v\n%s", err, out.String())
	}
	if resp.MatchedGoroutines < 1 {
		t.Fatalf("matched_goroutines = %d, want >= 1; resp = %+v", resp.MatchedGoroutines, resp)
	}
	if resp.ReachableBytes < 8<<20 {
		t.Fatalf("reachable_bytes = %d, want >= pinned payload", resp.ReachableBytes)
	}
	if resp.Labels[key] != value {
		t.Fatalf("response labels = %#v", resp.Labels)
	}

	out.Reset()
	errBuf.Reset()
	code = cli.Main([]string{"memusage", srv.URL, "-labels", key + "=" + value, "-format", "text"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("text cli exit code = %d\nstdout: %s\nstderr: %s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "  "+key+"="+value) ||
		!strings.Contains(out.String(), "matched_goroutines: ") {
		t.Fatalf("unexpected text output:\n%s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = cli.Main([]string{"bubbles", srv.URL}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("bubbles cli exit code = %d\nstdout: %s\nstderr: %s", code, out.String(), errBuf.String())
	}
	var bubbles memusage.BubblesResponse
	if err := json.Unmarshal(out.Bytes(), &bubbles); err != nil {
		t.Fatalf("decode bubbles output: %v\n%s", err, out.String())
	}
	found := false
	for _, bubble := range bubbles.Bubbles {
		if bubble.Labels[key] == value && bubble.GoroutineCount >= 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("bubbles output lacks %s=%s: %+v", key, value, bubbles.Bubbles)
	}
}

// TestBundle_ParityWithInProcessEndpoint runs the same workload through
// POST /debug/memusage and through bundle + AnalyzeDump and requires
// the same goroutine match count. Byte counts come from two separate
// dumps and may differ; match counts must not.
func TestBundle_ParityWithInProcessEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump integration test in short mode")
	}
	key := strings.Clone("job")
	value := strings.Clone("parity")
	stop := startLabeledGoroutine(t, pprof.Labels(key, value), 4<<20)
	defer stop()

	// In-process endpoint.
	rr := httptest.NewRecorder()
	MemUsageHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, MemUsagePath,
		strings.NewReader(`{"labels":{"`+key+`":"`+value+`"}}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("in-process endpoint status %d: %s", rr.Code, rr.Body.String())
	}
	var inProcess memusage.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &inProcess); err != nil {
		t.Fatalf("decode in-process response: %v", err)
	}

	// External path.
	b := captureBundle(t)
	external, err := analyzeOpenedBundle(t, b, map[string]string{key: value})
	if err != nil {
		t.Fatalf("AnalyzeDump: %v", err)
	}

	if inProcess.MatchedGoroutines != external.MatchedGoroutines {
		t.Fatalf("matched_goroutines parity broken: in-process %d, external %d",
			inProcess.MatchedGoroutines, external.MatchedGoroutines)
	}
	if external.ReachableBytes == 0 {
		t.Fatalf("external reachable_bytes = 0")
	}
	t.Logf("parity: matched=%d in-process bytes=%d external bytes=%d",
		inProcess.MatchedGoroutines, inProcess.ReachableBytes, external.ReachableBytes)
}

// TestRegister_MountsBothEndpoints is a cheap smoke test that Register
// wires both paths (no heap dump: bad requests only).
func TestRegister_MountsBothEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsagePath, nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s = %d, want 405", MemUsagePath, rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, MemUsageBundlePath, nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s = %d, want 405", MemUsageBundlePath, rr.Code)
	}
}
