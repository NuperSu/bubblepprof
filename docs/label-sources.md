# Label sources in bubblepprof

## Goal

`bubblepprof` reports heap memory usage for *pprof bubbles* — groups of
goroutines that share the same `runtime/pprof` labels. To do that it
needs to know, for every goroutine in a heap snapshot, which pprof
labels were attached to it.

The thesis-facing product is an in-process HTTP endpoint:

```http
POST /debug/memusage
Content-Type: application/json

{
  "labels": {
    "job": "42",
    "tenant": "acme"
  }
}
```

The endpoint returns the total shallow size of heap objects reachable
from non-system goroutines whose ordinary `runtime/pprof` labels contain
all requested key/value pairs.

This document describes where those labels come from.

## Required user labeling model

Profiled applications must use **standard `runtime/pprof` labels**:

```go
import "runtime/pprof"

pprof.Do(ctx, pprof.Labels("job", "42"), func(ctx context.Context) {
    // work attributed to the "job=42" bubble
})
```

The main profiler path must **not** require any of the following:

- `bubblepprof.Do`
- `bubblepprof.Labels`
- a `dynamicString(...)` helper around label literals
- a `labels.json` manifest
- correlation against a separately captured `goroutine.pprof`
- a modified Go toolchain

`pkg/bubblepprof` keeps a few wrapper helpers (`Do`, `Labels`,
`WithLabels`, `SetGoroutineLabels`, `Go`). They are optional compatibility
helpers — they call `runtime/pprof` internally and do not define a
separate labeling system. They are not part of the required path.

## Source priority

```
1. heap-native labels (primary, intended exact source)
   Source: heap.dump runtime.g.labels
   Path:
       heap dump goroutine record
           -> runtime.g address
           -> runtime.g.labels field (known runtime layout)
           -> runtime/pprof label map
           -> label.Set
           -> []Label
           -> key/value strings
   Default in /debug/memusage.

2. labels.json (fallback / debug only)
   Source: bubblepprof wrapper Registry snapshot.
   Status: optional fallback. Useful when heap-native recovery is
           unsupported on the running Go runtime/GOARCH. Requires
           application instrumentation; not required for the main
           profiler contract.

3. goroutine.pprof (diagnostic / explicit best-effort only)
   Source: separately captured runtime/pprof goroutine profile.
   Status: opt-in. Captured at a different runtime moment than
           heap.dump, so goroutine identity and labels may not align
           with the heap snapshot. Reports built from it are flagged as
           best-effort.
```

> **Warning.** `goroutine.pprof` must not silently drive exact heap
> reports. It is not guaranteed to represent the same goroutine
> set/state as the heap dump. Use it only as a diagnostic or as
> explicit, user-opted best-effort attribution.

### Why heap-native is primary

`runtime/debug.WriteHeapDump` runs with the world stopped and serializes
every `runtime.g`, including its `labels` pointer. The heap dump is the
authoritative snapshot of:

- heap objects and shallow sizes,
- object pointer fields,
- goroutine records,
- stack frames,
- stack roots,
- global / runtime roots.

For pprof-bubble attribution, labels must come from the same snapshot
that defines reachability. Decoding `runtime.g.labels` directly out of
the heap dump gives labels at exactly the moment the heap was captured,
so the answer to "which goroutines were labeled `job=42` when the heap
was sampled?" is internally consistent.

### Why `goroutine.pprof` is not exact

`goroutine.pprof` is collected after `WriteHeapDump` returns and the
world resumes. By the time the profile is taken:

- a goroutine seen in `heap.dump` may have exited,
- a new goroutine may have been created,
- a goroutine's stack and labels may have changed,
- the profile groups samples by identical stack+label, so multiple
  distinct heap goroutines that share a stack and labels collapse to
  one bucket.

A correct exact heap-usage report cannot be built from this source
without re-running the world. The resolver therefore only assigns
labels from the profile when the caller explicitly opts in.

### Why `labels.json` is a fallback

`labels.json` is emitted by the `bubblepprof` wrapper Registry. It
records `goid -> labels` near the time of the snapshot, but it depends
on the application using the wrapper API. It is exact in the goid-to-
labels sense, yet it is not part of the standard `runtime/pprof`
contract — so it cannot be the main profiler requirement. It exists as
a fallback for runtimes/layouts where heap-native decoding is not
supported and as a debugging aid.

## Why heap-native labels are hard

Heap-native recovery depends on **private Go runtime layout**:

- The `runtime.g` struct layout, including the offset of the `labels`
  field, is Go-version and GOARCH specific.
- The `runtime/pprof` label map type is internal and may change.
- Label *key/value strings* may not live inside heap objects. When the
  caller writes `pprof.Labels("job", "42")` with ordinary string
  literals, the resulting string headers point into the executable's
  read-only data segment, not heap memory. `WriteHeapDump` does not
  serialize those bytes as object contents.

Future phases need:

1. a **runtime layout provider** that maps `(Go version, GOARCH)` to
   `runtime.g` and label-map offsets,
2. a **process/executable memory reader** that can resolve string bytes
   outside heap objects, by reading the current process memory mappings
   (for the in-process endpoint) or executable load segments (for
   offline CLI use),
3. clear unsupported/error diagnostics when neither is available.

## Verified heap-native layouts

The current prototype has a verified layout only for a narrow target:

```
verified prototype layout
    Go 1.26.*
    amd64
    pointer size 8
    runtime.g.labels offset 0x160
```

This is **not** universal support. Other Go versions or architectures
need their own verified entries (or a runtime layout provider) before
heap-native recovery is reliable.

For unsupported layouts the resolver records `unsupported_runtime` and
falls back to `labels.json` if present (or refuses to assign labels if
the caller required heap-native).

Future layout resolution should use, in order of preference:

1. a verified Go-version/GOARCH layout table,
2. optional DWARF information read from the running executable,
3. a debug-only offset scanner for development.

## Executable / process memory

Ordinary pprof labels can use string literals:

```go
pprof.Labels("job", "42")
```

The resulting label map may contain string headers pointing to
read-only program data rather than heap-allocated bytes. Heap-native
recovery therefore needs a memory reader that can resolve string bytes
from:

1. retained heap dump object contents (where the bytes are heap-owned),
2. the current process's memory mappings (for the in-process endpoint),
3. the executable's load segments (for offline CLI debugging).

The current prototype only reads heap object contents. The wrapper APIs
clone label strings onto the heap as a workaround so existing examples
work, but this is a wrapper-side hack, not a property of the main
profiler contract. Removing the workaround is a future-phase task that
depends on the memory reader above.

## Snapshot artifact roles

Required:

- `heap.dump` — runtime heap dump (objects + roots + goroutines).
- `metadata.json` — snapshot metadata (Go version, PID, timestamp).

Fallback:

- `labels.json` — optional `goid -> labels` manifest emitted by the
  bubblepprof wrapper's `Registry`. Optional and not part of the main
  contract.

Diagnostics (best-effort only):

- `goroutine.pprof` — pprof goroutine profile, captured separately.
- `goroutine.stacks` — `debug=2` stack dump.

## Attribution modes

The bubble report tags itself with one of:

- `heap_native_exact` — only heap-native matches.
- `manifest_exact` — only labels.json matches.
- `mixed_exact_heap_and_manifest` — heap matches plus labels.json
  filling gaps.
- `best_effort_profile_fallback` — at least one label came from
  `goroutine.pprof`. Result is best-effort.
- `no_labels` — no labels resolved.

The future `/debug/memusage` endpoint should report `heap_native` as
the default attribution and only widen to other modes on explicit
opt-in.

## Snapshot CLI (development / offline tools)

The existing snapshot commands remain useful development and debugging
tools. They are **not** the final main-product interface.

```bash
# Inspect snapshot.tar metadata.
bubblepprof snapshot info snapshot.tar

# Parse heap.dump only.
bubblepprof snapshot parse snapshot.tar

# Parse heap.dump and build the object graph / reachability.
bubblepprof snapshot graph snapshot.tar

# Debug heap-native pprof label recovery.
bubblepprof snapshot heap-labels snapshot.tar

# Debug / inspect label resolution.
bubblepprof snapshot labels snapshot.tar

# Offline bubble report for development/testing.
bubblepprof snapshot bubbles snapshot.tar
bubblepprof snapshot bubbles --allow-profile-fallback snapshot.tar
bubblepprof snapshot bubbles --labels-source heap     snapshot.tar
bubblepprof snapshot bubbles --labels-source manifest snapshot.tar
bubblepprof snapshot bubbles --labels-source profile  snapshot.tar  # best-effort
bubblepprof snapshot bubbles --require-heap-labels    snapshot.tar
```

`snapshot heap-labels` is the validation tool for heap-native recovery
and supports `--g-labels-offset`, `--find-offset key=value`, and
`--show-failed` for layout debugging.

The thesis-facing target is the in-process `POST /debug/memusage`
endpoint, which will reuse the same parser, graph builder, and
heap-native label decoder.

## Wrapper API (optional)

`pkg/bubblepprof` exposes a small wrapper layer:

```go
bubblepprof.Labels("bubble", "alpha", "job", "42")
bubblepprof.Do(ctx, bubblepprof.Labels("bubble", "alpha"), fn)
bubblepprof.WithLabels(ctx, bubblepprof.Labels(...))
bubblepprof.SetGoroutineLabels(ctx)
bubblepprof.Go(ctx, fn)
```

These are **optional compatibility / fallback helpers**. They:

- call `runtime/pprof` internally so labels are visible to other Go
  tooling,
- as a side effect, clone label strings so they live on the heap and
  are recoverable by the current prototype's heap-native decoder,
- record entries into the `Registry` that feeds `labels.json`.

They are not required for the main profiler path. Phase 0 does not
remove them, but the documentation no longer treats them as the
primary API. Future phases plan to remove the heap-clone workaround
once a process-memory reader exists.

## Known limitations

- `runtime.g` layout is private; supported (Go version, GOARCH)
  combinations are narrow.
- Label key/value bytes from string literals may live outside heap
  objects and are not readable from `heap.dump` alone.
- The current verified prototype layout covers a single Go release
  (see above).
- `goroutine.pprof` is not exact and must remain opt-in.
- A `/debug/memusage` endpoint is not implemented yet; the current
  offline CLI is the only working entry point.

## What NOT to do

- Do not silently use `goroutine.pprof` to assign exact labels.
- Do not require users to write `dynamicString(...)`.
- Do not require the `bubblepprof` wrapper for the main path.
- Do not treat `labels.json` as part of the required user contract.
- Do not auto-scan `g.labels` offsets in normal bubble reports — that
  remains a `snapshot heap-labels --find-offset` debug mode.
- Do not expose profile correlation keys (`goid`, `goroutine_id`,
  `goroutine`) as normal bubble labels.
- Do not retain `Object.Contents` in `snapshot parse` / `snapshot
  graph`. Only `snapshot labels` / `snapshot bubbles` / `snapshot
  heap-labels` retain contents, and only when the source requires it.
