// Command bench is a thesis-grade measurement harness for bubblepprof's
// /debug/memusage pipeline. It spawns a parameterized synthetic workload,
// drives N iterations of memusage.Compute against the running process
// (in-process — no HTTP loopback noise), and emits a JSON report with
// per-iteration measurements and aggregate statistics.
//
// Captured per iteration:
//
//   - wall-clock latency of Compute
//   - max user-visible scheduling pause during Compute (heartbeat goroutine)
//   - Go heap allocated by Compute (HeapAlloc delta, TotalAlloc delta)
//   - process RSS before/after (/proc/self/status VmRSS, VmHWM)
//   - matched_goroutines, reachable_bytes from the response
//
// Aggregates: mean, stddev, min, max, p50, p95, p99 per metric.
//
// One iteration optionally runs under runtime/trace (-trace=path.trace) so
// the per-stage spans recorded by internal/memusage's trace.StartRegion
// calls can be inspected with `go tool trace` and cross-checked against
// the heartbeat-based STW estimate.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bubblepprof/internal/memusage"
)

type config struct {
	HeapMB        int     `json:"heap_mb"`
	Goroutines    int     `json:"goroutines"`
	MatchFraction float64 `json:"match_fraction"`
	GCPre         bool    `json:"gc_pre"`
	Iterations    int     `json:"iterations"`
	Warmup        int     `json:"warmup"`
	TracePath     string  `json:"trace_path,omitempty"`
	OutPath       string  `json:"out_path,omitempty"`
	Tag           string  `json:"tag,omitempty"`
}

type iterationResult struct {
	Index             int    `json:"index"`
	WallNanos         int64  `json:"wall_ns"`
	MaxHeartbeatNanos int64  `json:"max_heartbeat_pause_ns"`
	GoHeapAllocDelta  int64  `json:"go_heap_alloc_delta_b"`
	GoTotalAllocDelta int64  `json:"go_total_alloc_delta_b"`
	VmRSSBeforeKB     int64  `json:"vm_rss_before_kb"`
	VmRSSAfterKB      int64  `json:"vm_rss_after_kb"`
	VmHWMAfterKB      int64  `json:"vm_hwm_after_kb"`
	MatchedGoroutines int    `json:"matched_goroutines"`
	ReachableBytes    uint64 `json:"reachable_bytes"`
	UnderTrace        bool   `json:"under_trace,omitempty"`
}

type summary struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Stddev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
}

type goInfo struct {
	Version  string `json:"version"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	NumCPU   int    `json:"num_cpu"`
	GOMAXPROC int   `json:"gomaxprocs"`
}

type report struct {
	Config     config             `json:"config"`
	Go         goInfo             `json:"go"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt time.Time          `json:"finished_at"`
	Iterations []iterationResult  `json:"iterations"`
	Summary    map[string]summary `json:"summary"`
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.IntVar(&cfg.HeapMB, "heap-mb", 200, "resident user-heap target in MiB")
	flag.IntVar(&cfg.Goroutines, "goroutines", 1000, "labeled worker goroutines to spawn")
	flag.Float64Var(&cfg.MatchFraction, "match-fraction", 1.0, "fraction of workers whose labels match the query (0..1)")
	flag.BoolVar(&cfg.GCPre, "gc-pre", false, "set memusage.Options.GCBeforeHeapDump")
	flag.IntVar(&cfg.Iterations, "iterations", 20, "measured iterations")
	flag.IntVar(&cfg.Warmup, "warmup", 3, "warmup iterations (discarded)")
	flag.StringVar(&cfg.TracePath, "trace", "", "if set, record runtime/trace for one extra iteration to this path")
	flag.StringVar(&cfg.OutPath, "out", "", "JSON output path (default stdout)")
	flag.StringVar(&cfg.Tag, "tag", "", "arbitrary tag for this run (recorded in JSON)")
	flag.Parse()
	if cfg.MatchFraction < 0 {
		cfg.MatchFraction = 0
	}
	if cfg.MatchFraction > 1 {
		cfg.MatchFraction = 1
	}
	return cfg
}

func run(cfg config) error {
	ctx, cancel := context.WithCancel(context.Background())

	workersDone := spawnWorkload(ctx, cfg)
	hb := startHeartbeat(ctx)
	// Ordered cleanup: cancel() first so the workers and heartbeat see
	// ctx.Done; then wait for workers (wg.Wait) and the heartbeat goroutine
	// to actually exit. Separate defers in the wrong order deadlock —
	// wg.Wait blocks forever if cancel hasn't run yet.
	defer func() {
		cancel()
		workersDone()
		hb.stop()
	}()

	comp := memusage.NewComputer(memusage.Options{GCBeforeHeapDump: cfg.GCPre})
	defer comp.Close()

	req := memusage.Request{Labels: map[string]string{"job": "alpha"}}

	for i := 0; i < cfg.Warmup; i++ {
		if _, err := comp.Compute(ctx, req); err != nil {
			return fmt.Errorf("warmup iteration %d: %w", i, err)
		}
	}

	results := make([]iterationResult, 0, cfg.Iterations)
	startedAt := time.Now()
	for i := 0; i < cfg.Iterations; i++ {
		ir, err := runOne(ctx, comp, req, hb, i)
		if err != nil {
			return fmt.Errorf("iteration %d: %w", i, err)
		}
		results = append(results, ir)
	}

	if cfg.TracePath != "" {
		ir, err := runUnderTrace(ctx, comp, req, hb, cfg.TracePath, len(results))
		if err != nil {
			return fmt.Errorf("trace iteration: %w", err)
		}
		results = append(results, ir)
	}

	rep := report{
		Config:     cfg,
		Go:         collectGoInfo(),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
		Iterations: results,
		Summary:    summarize(results),
	}

	return emitJSON(cfg.OutPath, rep)
}

// runOne executes one measured Compute call and returns the captured metrics.
//
// A forced GC runs immediately before the measurement so prior iterations'
// parsed snapshot, graph, and label maps cannot accumulate and inflate the
// next Compute's wall time and HeapAlloc deltas. Without this, iterations
// drift upward and the thesis numbers become meaningless.
func runOne(
	ctx context.Context,
	comp *memusage.Computer,
	req memusage.Request,
	hb *heartbeat,
	idx int,
) (iterationResult, error) {
	runtime.GC()
	runtime.GC() // second pass: run finalizers queued by the first.
	vmRSSBefore, _ := readProcStatus()
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	hb.reset()
	start := time.Now()
	resp, err := comp.Compute(ctx, req)
	wall := time.Since(start)
	maxPause := hb.read()

	if err != nil {
		return iterationResult{}, err
	}

	var msAfter runtime.MemStats
	runtime.ReadMemStats(&msAfter)
	vmRSSAfter, vmHWMAfter := readProcStatus()

	return iterationResult{
		Index:             idx,
		WallNanos:         wall.Nanoseconds(),
		MaxHeartbeatNanos: maxPause,
		GoHeapAllocDelta:  int64(msAfter.HeapAlloc) - int64(msBefore.HeapAlloc),
		GoTotalAllocDelta: int64(msAfter.TotalAlloc) - int64(msBefore.TotalAlloc),
		VmRSSBeforeKB:     vmRSSBefore,
		VmRSSAfterKB:      vmRSSAfter,
		VmHWMAfterKB:      vmHWMAfter,
		MatchedGoroutines: resp.MatchedGoroutines,
		ReachableBytes:    resp.ReachableBytes,
	}, nil
}

func runUnderTrace(
	ctx context.Context,
	comp *memusage.Computer,
	req memusage.Request,
	hb *heartbeat,
	path string,
	idx int,
) (iterationResult, error) {
	f, err := os.Create(path)
	if err != nil {
		return iterationResult{}, fmt.Errorf("create trace file: %w", err)
	}
	defer f.Close()
	if err := trace.Start(f); err != nil {
		return iterationResult{}, fmt.Errorf("trace.Start: %w", err)
	}
	defer trace.Stop()

	tctx, task := trace.NewTask(ctx, "memusage_iter")
	defer task.End()
	ir, err := runOne(tctx, comp, req, hb, idx)
	if err != nil {
		return ir, err
	}
	ir.UnderTrace = true
	return ir, nil
}

// heartbeat is a goroutine that records the maximum wall-clock gap between
// consecutive samples in a tight time.Now() loop. The maximum is an upper
// bound on the longest scheduling pause any user goroutine experienced —
// during runtime/debug.WriteHeapDump's stop-the-world, the heartbeat is
// paused along with every other goroutine.
type heartbeat struct {
	maxNs atomic.Int64
	stopC chan struct{}
	doneC chan struct{}
}

func startHeartbeat(ctx context.Context) *heartbeat {
	hb := &heartbeat{
		stopC: make(chan struct{}),
		doneC: make(chan struct{}),
	}
	go func() {
		defer close(hb.doneC)
		last := time.Now()
		for {
			select {
			case <-hb.stopC:
				return
			case <-ctx.Done():
				return
			default:
			}
			now := time.Now()
			gap := now.Sub(last).Nanoseconds()
			if gap > hb.maxNs.Load() {
				hb.maxNs.Store(gap)
			}
			last = now
			runtime.Gosched()
		}
	}()
	return hb
}

func (h *heartbeat) reset()           { h.maxNs.Store(0) }
func (h *heartbeat) read() int64      { return h.maxNs.Load() }
func (h *heartbeat) stop()            { close(h.stopC); <-h.doneC }

// readProcStatus returns VmRSS and VmHWM in kB from /proc/self/status.
// Returns 0/0 if the file cannot be read (non-Linux fallback).
func readProcStatus() (vmRSSKB, vmHWMKB int64) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			vmRSSKB = parseKBLine(line)
		case strings.HasPrefix(line, "VmHWM:"):
			vmHWMKB = parseKBLine(line)
		}
	}
	return vmRSSKB, vmHWMKB
}

func parseKBLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func collectGoInfo() goInfo {
	return goInfo{
		Version:   runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		GOMAXPROC: runtime.GOMAXPROCS(0),
	}
}

// spawnWorkload launches cfg.Goroutines labeled worker goroutines that each
// retain a slice of heap so the resulting heap dump has realistic content.
// A fraction (cfg.MatchFraction) carry job=alpha (matches the query); the
// rest carry job=beta. Labels are heap-allocated via strings.Clone so the
// label bytes appear in heap object contents, making heap-native decoding
// work without depending on the in-process address-space reader.
//
// The returned function blocks until all workers have exited (after the
// caller cancels the context).
func spawnWorkload(ctx context.Context, cfg config) func() {
	if cfg.Goroutines <= 0 {
		return func() {}
	}

	totalBytes := int64(cfg.HeapMB) * (1 << 20)
	perWorker := totalBytes / int64(cfg.Goroutines)
	if perWorker < 1024 {
		perWorker = 1024
	}

	matchCount := int(math.Round(float64(cfg.Goroutines) * cfg.MatchFraction))
	if matchCount > cfg.Goroutines {
		matchCount = cfg.Goroutines
	}
	if matchCount < 0 {
		matchCount = 0
	}

	started := make(chan struct{}, cfg.Goroutines)
	var wg sync.WaitGroup
	wg.Add(cfg.Goroutines)

	alphaKey := strings.Clone("job")
	alphaVal := strings.Clone("alpha")
	betaVal := strings.Clone("beta")

	rng := rand.New(rand.NewSource(1))

	for i := 0; i < cfg.Goroutines; i++ {
		val := betaVal
		if i < matchCount {
			val = alphaVal
		}
		// Pre-clone shard key/value so heap-allocated label bytes match
		// the pprof-compatible API exactly.
		shardKey := strings.Clone("shard")
		shardVal := strings.Clone(strconv.Itoa(i % 32))
		size := perWorker
		seed := rng.Int63()
		pprof.Do(ctx, pprof.Labels(alphaKey, val, shardKey, shardVal), func(ctx context.Context) {
			go func() {
				defer wg.Done()
				pprof.SetGoroutineLabels(ctx)
				data := make([]byte, size)
				// Touch every page so the OS commits real RSS.
				for off := int64(0); off < size; off += 4096 {
					data[off] = byte(seed >> uint(off%56))
				}
				started <- struct{}{}
				<-ctx.Done()
				runtime.KeepAlive(data)
			}()
		})
	}

	// Wait until every worker has registered itself.
	for i := 0; i < cfg.Goroutines; i++ {
		<-started
	}
	runtime.GC()

	return func() { wg.Wait() }
}

// summarize computes per-metric aggregate statistics across all iterations.
func summarize(rs []iterationResult) map[string]summary {
	if len(rs) == 0 {
		return map[string]summary{}
	}
	collect := func(f func(iterationResult) float64) summary {
		vals := make([]float64, len(rs))
		for i, r := range rs {
			vals[i] = f(r)
		}
		return computeSummary(vals)
	}
	return map[string]summary{
		"wall_ns":                  collect(func(r iterationResult) float64 { return float64(r.WallNanos) }),
		"max_heartbeat_pause_ns":   collect(func(r iterationResult) float64 { return float64(r.MaxHeartbeatNanos) }),
		"go_heap_alloc_delta_b":    collect(func(r iterationResult) float64 { return float64(r.GoHeapAllocDelta) }),
		"go_total_alloc_delta_b":   collect(func(r iterationResult) float64 { return float64(r.GoTotalAllocDelta) }),
		"vm_rss_after_kb":          collect(func(r iterationResult) float64 { return float64(r.VmRSSAfterKB) }),
		"vm_hwm_after_kb":          collect(func(r iterationResult) float64 { return float64(r.VmHWMAfterKB) }),
		"reachable_bytes":          collect(func(r iterationResult) float64 { return float64(r.ReachableBytes) }),
	}
}

func computeSummary(values []float64) summary {
	n := len(values)
	if n == 0 {
		return summary{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(n)
	var sumSq float64
	for _, v := range sorted {
		d := v - mean
		sumSq += d * d
	}
	stddev := 0.0
	if n > 1 {
		stddev = math.Sqrt(sumSq / float64(n-1))
	}
	return summary{
		N:      n,
		Mean:   mean,
		Stddev: stddev,
		Min:    sorted[0],
		Max:    sorted[n-1],
		P50:    percentile(sorted, 0.50),
		P95:    percentile(sorted, 0.95),
		P99:    percentile(sorted, 0.99),
	}
}

// percentile returns the p-th percentile (0 ≤ p ≤ 1) of a pre-sorted slice
// using linear interpolation between adjacent ranks.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	rank := p * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func emitJSON(path string, rep report) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create out file: %w", err)
		}
		defer f.Close()
		enc = json.NewEncoder(f)
		enc.SetIndent("", "  ")
	}
	return enc.Encode(rep)
}
