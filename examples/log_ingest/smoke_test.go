package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/NuperSu/bubblepprof/pkg/bubblepprof"
)

// smokeDict is a package-level global so the dictionary it points at is
// reachable from a data/bss root, exactly like schemaRegistry in the example.
// That is what makes it surface as global_overlap below.
var smokeDict *labelDict

// TestLogIngestSmoke proves the headline behavior of the example on the
// critical path label -> endpoint -> JSON: a labeled ingester goroutine is
// matched, its private chunk ring counts as reachable_bytes, and the shared
// dictionary it references is reported as global_overlap that is strictly
// smaller than the total (i.e. the private chunks are NOT global).
//
// It drives the real ingesterLoop so the test exercises the same code and the
// same stack-rooted reachability the live example relies on.
func TestLogIngestSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump smoke test in short mode")
	}

	// Small but real: ~2 MiB shared dictionary, ~8 MiB private ring, so
	// reachable clearly exceeds the global overlap.
	smokeDict = buildDictionary(2)

	const (
		ringLen    = 8
		chunkBytes = 1 << 20
	)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var c counters

	labels := pprof.Labels(
		"service", "log-ingester",
		"tenant", "smoke-test-tenant",
		"stream", "app",
		"shard", "0",
	)
	spawnLabeled(ctx, &wg, labels, func(ctx context.Context) {
		ingesterLoop(ctx, smokeDict, ringLen, chunkBytes, 1, &c)
	})
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Wait until the ingester has pre-filled its ring (every chunk allocated).
	waitForResident(t, &c, uint64(ringLen)*chunkBytes)

	mux := http.NewServeMux()
	bubblepprof.RegisterMemUsage(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := bytes.NewReader([]byte(`{"labels":{"tenant":"smoke-test-tenant"}}`))
	resp, err := http.Post(srv.URL+bubblepprof.MemUsagePath, "application/json", body)
	if err != nil {
		t.Fatalf("POST /debug/memusage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var er struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&er)
		if er.Code == "unsupported_runtime" || er.Code == "string_missing" {
			t.Skipf("label recovery unavailable on this platform (code=%q); skipping smoke test", er.Code)
		}
	}

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, buf.String())
	}

	var result struct {
		MatchedGoroutines  int    `json:"matched_goroutines"`
		ReachableBytes     uint64 `json:"reachable_bytes"`
		GlobalOverlapBytes uint64 `json:"global_overlap_bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.MatchedGoroutines < 1 {
		t.Errorf("matched_goroutines = %d, want >= 1", result.MatchedGoroutines)
	}
	if result.ReachableBytes == 0 {
		t.Errorf("reachable_bytes = 0, want > 0")
	}
	// The shared dictionary is reachable from both the goroutine and the
	// smokeDict global, so it must register as global_overlap...
	if result.GlobalOverlapBytes == 0 {
		t.Errorf("global_overlap_bytes = 0, want > 0 (shared dictionary should overlap globals)")
	}
	// ...but it must be strictly less than the total, proving the private
	// chunk ring is attributed to the tenant and not counted as global.
	if result.GlobalOverlapBytes >= result.ReachableBytes {
		t.Errorf("global_overlap_bytes = %d >= reachable_bytes = %d; private chunks should not be global",
			result.GlobalOverlapBytes, result.ReachableBytes)
	}

	runtime.KeepAlive(smokeDict)
	t.Logf("smoke: matched=%d reachable_bytes=%d global_overlap_bytes=%d private~=%d",
		result.MatchedGoroutines, result.ReachableBytes, result.GlobalOverlapBytes,
		result.ReachableBytes-result.GlobalOverlapBytes)
}

// waitForResident blocks until the ingester has allocated at least want bytes,
// i.e. its chunk ring is fully resident before the heap dump is taken.
func waitForResident(t *testing.T, c *counters, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.bytesAlloc.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for resident ring: have %d bytes, want %d", c.bytesAlloc.Load(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
