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

	"bubblepprof/pkg/bubblepprof"
)

// TestOrderPipelineSmoke proves that a labeled goroutine created the same way
// as the example can be matched by /debug/memusage. It does not run the full
// simulation; it exercises the critical path: label -> endpoint -> JSON match.
func TestOrderPipelineSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live heap-dump smoke test in short mode")
	}

	ctx, cancel := context.WithCancel(context.Background())

	const hold = 2 << 20 // 2 MiB to give reachability something to count
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// Mirror exactly how the example stamps goroutine labels.
	go func() {
		defer wg.Done()
		innerCtx := pprof.WithLabels(ctx, pprof.Labels(
			"component", "ledger",
			"role", "tenant_aggregator",
			"tenant", "smoke-test-tenant",
		))
		pprof.SetGoroutineLabels(innerCtx)

		data := make([]byte, hold)
		for i := 0; i < len(data); i += 4096 {
			data[i] = 0xAB
		}
		close(started)
		<-ctx.Done()
		runtime.KeepAlive(data)
	}()
	<-started
	defer func() {
		cancel()
		wg.Wait()
	}()

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
		if er.Code == "unsupported_runtime" {
			t.Skipf("runtime not in verified layout table; skipping smoke test")
		}
	}

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, buf.String())
	}

	var result struct {
		MatchedGoroutines int    `json:"matched_goroutines"`
		ReachableBytes    uint64 `json:"reachable_bytes"`
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
	t.Logf("smoke: matched=%d reachable_bytes=%d", result.MatchedGoroutines, result.ReachableBytes)
}
