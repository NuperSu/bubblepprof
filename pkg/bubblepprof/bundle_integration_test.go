package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"github.com/NuperSu/bubblepprof/internal/addrspace"
	"github.com/NuperSu/bubblepprof/internal/bundle"
	"github.com/NuperSu/bubblepprof/internal/cli"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
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
	if !strings.Contains(out.String(), `  "`+key+`"="`+value+`"`) ||
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

func TestBundleReusableSessionParity(t *testing.T) {
	if testing.Short() {
		t.Skip("live heap dump")
	}
	key, value := strings.Clone("session"), strings.Clone("parity")
	stop := startLabeledGoroutine(t, pprof.Labels(key, value), 1<<20)
	defer stop()
	b := captureBundle(t)
	labels := map[string]string{key: value}
	want, err := analyzeOpenedBundle(t, b, labels)
	if err != nil {
		t.Fatal(err)
	}
	s, err := offline.LoadBundle(context.Background(), b, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Loading consumes all lazy content reads. Queries need no bundle files.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := s.Query(context.Background(), memusage.Request{Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	if s.Metadata().Graph.Objects == 0 || len(s.GoroutineIDs(key, value)) == 0 {
		t.Fatal("missing metadata or labels")
	}
}

func TestBundleExploreBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("live heap dump")
	}
	key, value := strings.Clone("batch"), strings.Clone("parity")
	stop := startLabeledGoroutine(t, pprof.Labels(key, value), 1<<20)
	defer stop()
	rr := httptest.NewRecorder()
	BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.tar")
	queries := filepath.Join(dir, "queries.json")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queries, []byte(`[{"labels":{"batch":"parity"}},{"labels":{"batch":"absent"}},{"labels":{"batch":"parity"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := cli.Main([]string{"explore", path, "-queries", queries}, &out, &stderr); code != 0 {
		t.Fatalf("%d: %s %s", code, &out, &stderr)
	}
	var got struct {
		Results  []memusage.Response
		Metadata memusage.SnapshotMetadata
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 3 || !reflect.DeepEqual(got.Results[0], got.Results[2]) || got.Results[0].MatchedGoroutines == 0 || got.Results[1].MatchedGoroutines != 0 || got.Metadata.Graph.Objects == 0 {
		t.Fatalf("incorrect batch: %+v", got)
	}
	s, err := offline.OpenSnapshot(context.Background(), path, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want, err := s.Query(context.Background(), memusage.Request{Labels: map[string]string{key: value}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*want, got.Results[0]) {
		t.Fatal("batch differs from session")
	}
}

func TestBundleMultiBubbleCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("live heap dump")
	}
	stop := startLabeledGoroutine(t, pprof.Labels(strings.Clone("tenant"), strings.Clone("phase2"), strings.Clone("role"), strings.Clone("phase2-worker")), 1<<20)
	defer stop()
	rr := httptest.NewRecorder()
	BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	path := filepath.Join(t.TempDir(), "capture.tar")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := offline.OpenSnapshot(context.Background(), path, memusage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := memusage.Request{Labels: map[string]string{"tenant": "phase2"}}
	b := memusage.Request{Labels: map[string]string{"role": "phase2-worker"}}
	comparison, err := s.Compare(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Shared.Bytes == 0 || len(comparison.OverlappingGoroutines) == 0 {
		t.Fatalf("%+v", comparison)
	}
	top, err := s.Top(context.Background(), memusage.TopOptions{By: "tenant", Sort: "shared", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := s.Exclusive(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.Shared(context.Background(), a, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	query, err := s.Query(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if exclusive.Reachable.Bytes != query.ReachableBytes || exclusive.Reachable.Objects != query.ReachableObjects {
		t.Fatal("query parity")
	}
	cases := []struct {
		command string
		args    []string
		want    any
	}{
		{"top", []string{"--by", "tenant", "--sort", "shared", "--limit", "1"}, top},
		{"compare", []string{"--a", "tenant=phase2", "--b", "role=phase2-worker"}, comparison},
		{"exclusive", []string{"--labels", "tenant=phase2"}, exclusive},
		{"shared", []string{"--a", "tenant=phase2", "--b", "role=phase2-worker", "--limit", "0"}, shared},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			args := append([]string{tc.command, path, "--format", "json"}, tc.args...)
			var out, stderr bytes.Buffer
			if code := cli.Main(args, &out, &stderr); code != 0 {
				t.Fatalf("%d: %s %s", code, &out, &stderr)
			}
			var got struct {
				Result   json.RawMessage
				Metadata memusage.SnapshotMetadata
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			var gv, wv any
			json.Unmarshal(got.Result, &gv)
			json.Unmarshal(want, &wv)
			if !reflect.DeepEqual(gv, wv) || got.Metadata.Graph.Objects == 0 {
				t.Fatalf("CLI differs from reusable session: %s", &out)
			}
			out.Reset()
			args = append([]string{tc.command, path}, tc.args...)
			if code := cli.Main(args, &out, &stderr); code != 0 || !strings.Contains(out.String(), "validation:") {
				t.Fatalf("text output: %d %s %s", code, &out, &stderr)
			}
		})
	}
}
