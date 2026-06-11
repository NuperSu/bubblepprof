# Label sources in bubblepprof

## Goal

`bubblepprof` reports heap memory usage for *pprof bubbles* — groups of
goroutines that share the same `runtime/pprof` labels. To do that it
needs to know, for every goroutine in a heap snapshot, which pprof
labels were attached to it.

The product has two query surfaces: an in-process HTTP endpoint and an
external analyser (the `bubblepprof` CLI working from a capture bundle
served by `GET /debug/memusage/bundle`). Both run the same label
recovery described here. The in-process endpoint is:

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

The main profiler path does **not** require any of the following:

- a custom labeling wrapper
- a `dynamicString(...)` helper around label literals
- a `labels.json` manifest
- correlation against a separately captured `goroutine.pprof`
- a modified Go toolchain

`pkg/bubblepprof` exposes `/debug/memusage` and the optional
`/debug/memusage/bundle` capture endpoint. It does not provide or
require a custom labeling API.

## Label source

`/debug/memusage` uses **heap-native label recovery** only.

```
heap dump goroutine record
    -> runtime.g address
    -> runtime.g.labels field (known runtime layout)
    -> runtime/pprof label map
    -> label.Set
    -> []Label
    -> key/value strings
```

Labels and heap reachability come from the same stopped-world snapshot
(`runtime/debug.WriteHeapDump`), so the answer to "which goroutines were
labeled `job=42` when the heap was sampled?" is internally consistent.

Heap-native recovery is the only label source: no separately captured
goroutine profiles, no wrapper manifests, no label sidecar files.

If heap-native recovery fails or is incomplete, the endpoint returns an
explicit error — it never silently falls back to an alternative source or
returns a partial count as success.

### Why heap-native

`runtime/debug.WriteHeapDump` runs with the world stopped and serializes
every `runtime.g`, including its `labels` pointer. The heap dump is the
authoritative snapshot of:

- heap objects and shallow sizes,
- object pointer fields,
- goroutine records,
- stack frames,
- stack roots,
- global / runtime roots.

Decoding `runtime.g.labels` directly out of the heap dump gives labels
at exactly the moment the heap was captured.

## Why heap-native labels are hard

Heap-native recovery depends on **private Go runtime layout**:

- The `runtime.g` struct layout, including the offset of the `labels`
  field, is Go-version and pointer-size specific.
- The `runtime/pprof` label map type is internal and may change.
- Label *key/value strings* may not live inside heap objects. When the
  caller writes `pprof.Labels("job", "42")` with ordinary string
  literals, the resulting string headers point into the executable's
  read-only data segment, not heap memory. `WriteHeapDump` does not
  serialize those bytes as object contents.

## Verified heap-native layouts

The current prototype has verified layouts for:

```
Go 1.24.* – 1.26.*
64-bit little-endian (amd64, arm64): runtime.g.labels offsets 0x160 / 0x158 / 0x160
32-bit little-endian (arm, 386):     runtime.g.labels offsets 0xd4 / 0xd0 / 0xd8
```

Because `runtime.g` field offsets depend only on pointer width (not
architecture), a single table entry covers all 64-bit LE platforms and another
covers all 32-bit LE platforms for a given Go version.

This is **not** universal support. Other Go versions need their own
verified entries (or a runtime layout provider) before heap-native
recovery is reliable.

When the runtime layout is unsupported, the endpoint returns
`unsupported_runtime` without building the object graph.

## Executable / process memory

Ordinary pprof labels can use string literals:

```go
pprof.Labels("job", "42")
```

The resulting label map may contain string headers pointing to
read-only program data rather than heap-allocated bytes. Heap-native
recovery reads string bytes through a composite address-space reader
(`internal/addrspace`) that tries the following sources in order:

1. retained heap dump object contents (where the bytes are heap-owned),
2. read-only program memory — in-process via `addrspace.ProcessReader`
   (`/proc/self/mem` on Linux; on FreeBSD `/proc/self/mem` first with an
   on-disk ELF fallback (non-PIE only) when procfs is absent;
   ASLR-corrected Mach-O segments on macOS; ASLR-corrected PE sections on
   Windows/Wine), or, in the external analyser, the bundle's saved
   read-only segment snapshot served by `bundle.SegmentsReader`,
3. the executable's load segments (`addrspace.ELFReader` — an internal
   library reader; `/debug/memusage` does not expose this as a
   user-facing option).

`/debug/memusage` opens the process memory reader by default on Linux,
macOS, Windows, and FreeBSD. When the reader is unavailable (unsupported
platform, denied access, or `DisableProcessMemoryReader=true`) the endpoint
returns `422 string_missing` with a descriptive warning, never as a silent
fallback.

## Required artifacts

`/debug/memusage` requires only:

- `heap.dump` — runtime heap dump captured in-process.

The external analyser requires only a capture bundle — the same heap
dump plus the target's read-only segment snapshot and metadata, in one
tar stream.

No `labels.json`. No `goroutine.pprof`. No wrapper manifest.

## labeloffsetprobe (development only)

`cmd/labeloffsetprobe` is a development binary for verifying the
`runtime.g.labels` offset on the current Go runtime. It is not part of
the product interface.

When exactly one candidate offset is found, the tool prints a ready-to-paste
Go struct literal — a `runtimelayout.TableEntry` with the correct
`VersionPrefix`, `PtrSize`, `BigEndian`, and `GLabelsOffset` — that can be
copied directly into `internal/runtimelayout/table.go`.

```bash
go run ./cmd/labeloffsetprobe
```

## Known limitations

- `runtime.g` layout is private; supported (Go version, pointer size)
  combinations are narrow.
- Label key/value bytes from string literals are read from the running
  process address space (Linux/FreeBSD: `/proc/self/mem`; macOS: ASLR-corrected
  Mach-O segments; Windows/Wine: ASLR-corrected PE sections); on unsupported hosts or when
  `DisableProcessMemoryReader` is set, literal-allocated labels return
  `string_missing`.
- `addrspace.ELFReader` covers non-PIE binaries; PIE/ASLR binaries
  require a load bias that the snapshot does not yet record.
  `/debug/memusage` does not expose ELF reading.

## What NOT to do

- Do not silently use `goroutine.pprof` to assign labels.
- Do not require users to write `dynamicString(...)`.
- Do not require the `bubblepprof` wrapper for the main path.
- Do not use `labels.json` as part of the required user contract.
- Do not auto-scan `g.labels` offsets in normal requests — that
  remains a `labeloffsetprobe` debug mode.
