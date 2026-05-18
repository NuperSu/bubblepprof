# bubblepprof

`bubblepprof` is an in-process Go heap profiler that reports memory usage per **pprof bubble** — a group of goroutines that share the same [`runtime/pprof`](https://pkg.go.dev/runtime/pprof) labels.

## What it does

The standard Go profiling tools (`net/http/pprof`, `runtime/pprof`) answer process-level questions: total heap size, allocation hotspots, goroutine count. They do not answer per-workload questions like "how much heap is my `tenant=acme` checkout job holding right now?"

`bubblepprof` answers that question via a single endpoint:

```
POST /debug/memusage
```

It captures a heap dump, recovers pprof labels from goroutine runtime state, and returns the shallow size of all heap objects reachable from goroutines whose labels match your selector.

## How it differs from net/http/pprof

| | `net/http/pprof` | `bubblepprof` |
|---|---|---|
| Unit | process | label-selected goroutine group |
| Question | where are allocations coming from? | how much heap does job/tenant X hold? |
| Mechanism | sampling (allocs/heap) | stop-the-world heap dump + BFS |
| Requires instrumentation? | no | no (uses existing pprof labels) |
| Cost per query | low | high (stop-the-world) |

## Registering the endpoint

```go
import "bubblepprof/pkg/bubblepprof"

mux := http.NewServeMux()
bubblepprof.RegisterMemUsage(mux) // mounts at /debug/memusage
```

For custom options:

```go
h := bubblepprof.MemUsageHandlerWithOptions(bubblepprof.MemUsageOptions{
    DisableGCBeforeHeapDump: false, // default: GC before dump
})
mux.Handle(bubblepprof.MemUsagePath, h)
```

## Labeling work

Use the standard [`runtime/pprof`](https://pkg.go.dev/runtime/pprof) API. No bubblepprof wrapper is required.

```go
import (
    "context"
    "runtime/pprof"
)

pprof.Do(ctx, pprof.Labels("job", "42", "tenant", "acme"), func(ctx context.Context) {
    runJob(ctx)
})
```

For long-lived goroutines, stamp labels at goroutine start:

```go
go func() {
    ctx := pprof.WithLabels(parent, pprof.Labels("role", "worker", "tenant", t))
    pprof.SetGoroutineLabels(ctx)
    runWorker(ctx)
}()
```

## Querying memory usage

```bash
# How much heap do tenant=acme goroutines hold?
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"tenant":"acme"}}' | jq .
```

Multiple labels narrow the match (AND semantics):

```bash
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"tenant":"acme","tier":"enterprise"}}' | jq .
```

## Response fields

Success (`200 OK`):

```json
{
  "labels": {"tenant": "acme"},
  "matched_goroutines": 3,
  "reachable_objects": 18420,
  "reachable_bytes": 73400320,
  "global_overlap_objects": 12,
  "global_overlap_bytes": 49152,
  "system_overlap_objects": 3,
  "system_overlap_bytes": 12288
}
```

| Field | Meaning |
|---|---|
| `matched_goroutines` | Number of non-system goroutines whose labels contained every requested key/value pair |
| `reachable_objects` | Heap objects reachable from the union of matched goroutine roots (single BFS) |
| `reachable_bytes` | Sum of shallow sizes of those objects |
| `global_overlap_objects` | Objects in `reachable_objects` that are also reachable from global/data/bss roots |
| `global_overlap_bytes` | Bytes for those globally shared objects |
| `system_overlap_objects` | Objects shared with system/background goroutines |
| `system_overlap_bytes` | Bytes for those system-shared objects |

Diagnostic fields (`go_version`, `goarch`, `warnings`) appear on error responses only, not on successful measurements.

Error (`422 Unprocessable Entity`):

```json
{
  "error": "runtime.g.labels layout not in verified table",
  "code": "unsupported_runtime",
  "go_version": "go1.25",
  "goarch": "arm64",
  "warnings": []
}
```

The `code` field distinguishes failure modes:

| Code | Meaning |
|---|---|
| `unsupported_runtime` | No known `runtime.g.labels` layout for this Go version / arch |
| `string_missing` | Label structures found but key/value string bytes unavailable (e.g. literal labels on a platform without a process reader) |
| `label_recovery_failed` | Heap-native label decode failed for other structural reasons (e.g. `g_object_missing`, `malformed`) |
| `capture_failed` | Could not write the heap dump |
| `parse_failed` | Heap dump could not be parsed |
| `busy` | Another request is already running |

## Running the demo

```bash
go run ./examples/order_pipeline
```

Then in another terminal:

```bash
# Query memory held by the atlas-bikes tenant aggregator
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"tenant":"atlas-bikes"}}' | jq .

# Query memory held by notification workers
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"component":"async","role":"notification_worker"}}' | jq .

# A missing label returns zero matches, not an error
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"tenant":"nonexistent"}}' | jq .
```

The example also exposes `/debug/pprof` and `/stats`:

```bash
curl http://127.0.0.1:6060/stats
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

## Load-test example (`profiler_load`)

`profiler_load` is a stress test that sustains a large number of labeled goroutines
and a configurable resident heap, giving `/debug/memusage` meaningful bytes to attribute
to each bubble.

```bash
# Default: 768 workers, 160 MiB pinned resident heap
go run ./examples/profiler_load

# Heavier load: 1024 workers, 500 MiB pinned heap, 2-minute run
go run ./examples/profiler_load -mem-mb 500 -workers 1024 -duration 2m
```

Six worker types are distributed round-robin — each carries a `role` label,
a `pool` label (`compute`, `memory`, or `pipeline`), and a `shard` label:

```bash
# Heap reachable from all compute workers (cpu-hash + sorter)
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"pool":"compute"}}' | jq .

# Heap reachable from allocator workers (high churn pool)
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"role":"allocator"}}' | jq .

# Heap reachable from heap-scan workers (resident heap visible from matched roots)
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"role":"heap-scan"}}' | jq .

# Pipeline workers (channel producers + mutex consumers)
curl -s -X POST http://127.0.0.1:6060/debug/memusage \
  -H 'Content-Type: application/json' \
  -d '{"labels":{"pool":"pipeline"}}' | jq .
```

The process prints goroutine count, heap stats, and ops/sec every two seconds.
Use it to stress-test label recovery and reachability performance on realistic heap sizes.

## Security and performance

`/debug/memusage` is equivalent in sensitivity to `/debug/pprof/heap`. Every call:

- stops all goroutines (`runtime/debug.WriteHeapDump`),
- writes a full heap dump to a temporary file,
- reads the file back, and
- deletes the temporary file.

Latency is proportional to live heap size. Concurrent callers receive `429 Too Many Requests`.

**Protect the endpoint with the same controls you apply to `/debug/pprof`.** Do not expose it to untrusted callers.

## Current limitations

See [`docs/limitations.md`](docs/limitations.md) for a complete list. Key points:

- Heap-native label recovery is verified for **go1.24.\*–go1.26.\*** on Linux, macOS, Windows, and FreeBSD (amd64, arm64, arm, 386). Experimental tip (go1.27-devel) support is tested in CI but not required. Other Go versions return `unsupported_runtime`.
- Ordinary string literal labels are recovered via the in-process reader on Linux, macOS, FreeBSD, and Windows. On other platforms they may return `string_missing`.
- Sizes are **shallow** (the object itself, not transitive) and counts are **BFS-reachable** from the matched goroutine roots, not total process heap.
- Global and system overlap is reported separately; it is not subtracted automatically.

## Development tools

```bash
# Probe the correct runtime.g.labels offset for the current Go runtime
go run ./cmd/labeloffsetprobe

# Run all tests
go test ./...

# Vet
go vet ./...
```
