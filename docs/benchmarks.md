# Benchmarks

This document describes how `bubblepprof`'s target-side profiling paths are
measured for the thesis: what is measured, how, and how to reproduce.

## What is measured

`cmd/bench` has two measurement modes:

| Mode | Flag | What it measures in the target process |
| --- | --- | --- |
| In-process analysis | `-mode compute` | `memusage.Compute`: heap dump, lazy parse, label recovery, graph build, selected-root BFS |
| External analyser target | `-mode bundle` | `bundle.CaptureSelf`: heap dump plus rodata snapshot streamed as a bundle; no parse, graph build, or BFS in the target |

Per-iteration metrics captured by `cmd/bench`:

| Metric | What it is | Source |
| --- | --- | --- |
| `wall_ns` | End-to-end wall-clock time of `memusage.Compute` | `time.Now()` |
| `max_heartbeat_pause_ns` | Max gap observed by a tight-loop heartbeat goroutine during the call. Upper bound on the longest scheduling pause any user goroutine saw — i.e. the stop-the-world pause caused by `runtime/debug.WriteHeapDump`. | Heartbeat goroutine |
| `go_heap_alloc_delta_b` | `HeapAlloc` delta around `Compute` (Go-managed live heap) | `runtime.ReadMemStats` |
| `go_total_alloc_delta_b` | `TotalAlloc` delta (cumulative bytes allocated) | `runtime.ReadMemStats` |
| `vm_rss_after_kb` / `vm_hwm_after_kb` | Process resident set after the call; high-water mark | `/proc/self/status` |
| `vm_peak_delta_kb` | Per-call peak RSS growth when Linux `clear_refs` can reset `VmHWM`; otherwise zero or a lower-fidelity cumulative delta | `/proc/self/clear_refs`, `/proc/self/status` |
| `matched_goroutines`, `reachable_bytes` | Sanity-check fields from the JSON response in `compute` mode | endpoint output |
| `bundle_bytes` | Bundle tar bytes emitted in `bundle` mode | counting discard writer |

For per-stage timing (`capture` / `parse` / `labels` / `build` /
`compute_from_analysis`) the binary writes one `runtime/trace` capture per
configuration via `-trace <path>`. Open with `go tool trace`.

Per-configuration aggregates emitted in the JSON `summary` map: `mean`,
`stddev`, `min`, `max`, `p50`, `p95`, `p99`.

Independent OS-level cross-check via `/usr/bin/time -v`: `Maximum resident set
size`, user CPU, system CPU, major/minor page faults, voluntary/involuntary
context switches. Parsed by `bench/aggregate.py` into the summary CSV.

## Why a heartbeat for STW

`runtime/debug.WriteHeapDump` calls `runtime.stopTheWorld` directly. Its
pause is **not** reflected in `MemStats.PauseNs` (which only covers GC
phases). The heartbeat goroutine — a tight loop sampling `time.Now()` and
recording the max consecutive-sample gap — registers any scheduling pause,
including the WriteHeapDump STW. Cross-checked against a `runtime/trace`
capture, which records STW spans explicitly.

## Inter-iteration cleanup

By default, `cmd/bench` runs two passes of `runtime.GC()` immediately before
each measurement so the Go heap from the previous iteration (parsed snapshot,
graph, label map) cannot accumulate and inflate the next call's WriteHeapDump
size and wall time. Without this, iterations drift upward because Go does not
eagerly reclaim large structures between calls.

The cost of the inter-iteration GC is excluded from the measurement window.
Live rotating-workload runs use `-pre-measure-gc=false` so the process keeps
normal allocation pressure and GC pacing.

## Workload models

`cmd/bench` has two workload models:

| Model | Flag | What it means |
| --- | --- | --- |
| Static | `-workload static` | Each labeled goroutine retains one slice and then goes idle. This isolates the endpoint pipeline against a stable heap. |
| Rotating | `-workload rotating` | Each labeled goroutine retains a fixed-size ring and periodically replaces chunks. Resident heap stays roughly flat while allocation churn drives normal GC behavior, closer to `examples/log_ingest`. |

Use static results for algorithmic cost and allocator pressure. Use rotating
results for service-like RSS behavior. Compare `compute` and `bundle` rows for
the same workload/configuration to quantify target RSS reduction from the
external analyser.

## Configuration sweep

| Knob | Quick | Full |
| --- | --- | --- |
| `heap-mb` | 50, 200 | 50, 200, 500, 1000, 2000 |
| `goroutines` | 100, 1 000 | 100, 1 000, 5 000, 10 000 |
| `match-fraction` | 1.0 | 0.01, 0.5, 1.0 |
| `gc-pre` | false | false, true |
| iterations × warmup | 5 × 2 | 20 × 3 |

`match-fraction` controls the fraction of workers whose pprof labels match
the query (`job=alpha`). The rest carry `job=beta`. Sweeping this isolates
the cost of the per-query BFS from the cost of the structural graph build.

## Reproducing

Requires Linux + `/usr/bin/time` (GNU time) + `python3`.

```bash
# quick: 4 static configs (2 heap × 2 goroutines), validates end-to-end
bash bench/run.sh --quick

# full: static thesis sweep (120 configs × 20 iterations, several hours)
bash bench/run.sh --full

# live variants: rotating workload, no pre-measurement GC
bash bench/run.sh --quick-live
bash bench/run.sh --full-live

# target RSS comparison: in-process analysis vs external-analyser target capture
BENCH_MODES="compute bundle" bash bench/run.sh --quick
BENCH_MODES="compute bundle" bash bench/run.sh --quick-live

# inspect a single configuration:
go run ./cmd/bench -mode compute -heap-mb 500 -goroutines 1000 \
  -iterations 20 -warmup 3 \
  -trace bench/results/single.trace \
  -out bench/results/single.json

go run ./cmd/bench -mode bundle -heap-mb 500 -goroutines 1000 \
  -iterations 20 -warmup 3 \
  -out bench/results/single-bundle.json
```

Results land in `bench/results/`:
- `<tag>.json` — full iteration list + summary per configuration
- `<tag>.time.txt` — raw `/usr/bin/time -v` output
- `<tag>.trace` — runtime/trace capture for one iteration (open with `go tool trace`)
- `summary.csv` — aggregated table across all configurations

## Stage-level micro-benchmarks

`go test -bench` benchmarks under `internal/memusage/` give the classic
`ns/op`, `B/op`, `allocs/op` numbers per pipeline stage against a fixture
heap captured once in-process:

```bash
go test -bench=. -benchmem -run=^$ ./internal/memusage/
```

| Benchmark | What it measures |
| --- | --- |
| `BenchmarkWriteHeapDump/heap=NMB` | Live `runtime/debug.WriteHeapDump` under N MiB retained heap |
| `BenchmarkParse` | `heapdump.Parse` of a captured fixture (in-memory reader) |
| `BenchmarkBuildGraph` | `snapshotgraph.Build` of a parsed snapshot |
| `BenchmarkRecoverLabels` | `DefaultLabelRecoverer.Recover` (heap-native label decode) |
| `BenchmarkReachableFromGoroutines` | Single-BFS union from all non-system goroutines |
| `BenchmarkComputeEndToEnd` | Full `Computer.Compute` pipeline against the live runtime |

These complement `cmd/bench`: the Go bench reports `ns/op` and `allocs/op`
which `cmd/bench` does not; `cmd/bench` reports RSS, STW pause, and
cross-process aggregates which the Go bench cannot.

Note: `BenchmarkParse` and `BenchmarkRecoverLabels` use the eager
`KeepObjectContents=true` parse path, which retains all object bytes in memory.
The production `Computer.Compute` path uses `ParseLazyContents`, which reads
object bytes on demand without retaining them. Both paths produce identical
label results (verified by the lazy-parity test), but the stage benchmarks
measure slightly higher peak allocation than the production pipeline.

## Caveats

- **Linux only** — `/proc/self/status` and `/usr/bin/time -v` are required.
  `cmd/bench` will still run elsewhere but RSS columns will be zero.
- **Inter-iteration GC removes drift but adds cost outside the window.** In
  static mode, the drift it removes is artifactual (stale objects from previous
  Computes). Rotating live mode disables this cleanup to preserve normal GC
  pacing.
- **`VmHWM` must be reset for per-call peak RSS.** `cmd/bench` defaults to
  `-reset-vmhwm=true`, which uses Linux `echo 5 > /proc/self/clear_refs`.
  If the kernel does not support this, `vm_hwm_after_kb` remains a process
  lifetime high-water mark and `vm_peak_delta_kb` is not a reliable per-call
  peak.
- **`match-fraction=0.01`** still walks the graph from at least one matched
  goroutine's roots; it does not measure zero-match because `0.01 × N` rounds
  to at least one worker for every goroutine count in the sweep. Use
  `match-fraction=0` explicitly to benchmark the zero-match path.
- **`cmd/bench` defaults to `gc-pre=false`**, while the public `MemUsageHandler`
  defaults to GC before the dump. The full sweep includes both modes; the
  `gc_pre=false` rows isolate the pipeline cost without the pre-dump GC pause,
  and `gc_pre=true` rows reflect the production default.
- **`bundle` mode is target-side only.** It measures the process serving
  `GET /debug/memusage/bundle`: heap dump capture, rodata snapshot, and bundle
  streaming. It intentionally excludes offline analyser RSS/CPU, because that
  work happens in a separate process in production.
- **Heartbeat ≠ exact STW.** The heartbeat upper-bounds STW with scheduling
  jitter on top; cross-check against the `.trace` file. Discrepancy of a few
  hundred microseconds is normal.
- **Process reader** (`/proc/self/mem`) is opened lazily on the first
  `Compute` call; the warmup iterations cover that one-off cost.
- **Trace iteration excluded from summary.** When `-trace` is set, the traced
  iteration is stored in the JSON `iterations` array with `under_trace: true`
  but is excluded from the `summary` statistics. Trace overhead would otherwise
  inflate mean/p50/p95/p99.

## Headline results

To be filled in after `bench/run.sh --full` completes. Cite
`bench/results/summary.csv` and selected `.trace` files in the thesis.
