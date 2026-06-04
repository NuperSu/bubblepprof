# Algorithm: POST /debug/memusage

This document describes the exact algorithm executed on each call to `/debug/memusage`.

## Overview

The endpoint answers: *how much heap is reachable from non-system goroutines whose pprof labels match this selector?*

It answers from a single consistent snapshot: labels and heap graph come from the same stopped-world heap dump.

## Pipeline

### Step 1 — Validate the request

Parse the JSON body. Reject requests with:
- missing or empty `labels` map (`400 empty_labels`)
- too many label entries (`400 too_many_labels`)
- empty label key (`400 empty_label_key`)
- key or value exceeding configured byte limits (`400 label_*_too_long`)

Validation is purely structural; no heap activity.

### Step 2 — GC (optional)

If `GCBeforeHeapDump` is enabled (the default), `runtime.GC()` runs to reduce dead objects in the dump. This does not affect correctness, only the size of the snapshot.

### Step 3 — Capture and parse a heap dump

`runtime/debug.WriteHeapDump` stops all goroutines and serializes the entire heap to a temporary file. The stop-the-world pause is proportional to live heap size.

The dump is parsed with `heapdump.ParseLazyContents`: object content bytes are **not** retained in the Go heap. Instead, a `ContentResolver` records each object's byte range within the dump file; the label decoder fetches content bytes on demand via `io.ReaderAt`. This avoids doubling the process RSS during parsing.

After all processing, the temporary file is deleted.

### Step 4 — Open the process string-body reader

An in-process reader is opened to recover string literal bytes that live in the executable's read-only data segment rather than inside heap objects. The reader is platform-specific:

- **Linux / FreeBSD**: `/proc/self/maps` is parsed for readable mappings; `/proc/self/mem` is opened for random reads. FreeBSD falls back to ELF rodata when procfs is unavailable.
- **macOS**: the current executable's Mach-O segments are parsed and read with ASLR slide correction.
- **Windows** (including Wine): the current executable's PE sections are parsed and read with ASLR slide correction.

If the reader cannot be opened (unsupported platform, access denied, or `DisableProcessMemoryReader` set), the endpoint continues with heap-object contents only and adds a warning. Literal-allocated labels may later fail with `string_missing`.

### Step 5 — Decode pprof labels and look up the runtime layout

The heap dump parameters contain the exact Go build version and pointer size. The label decoder uses these to consult a static table of verified `runtime.g.labels` field offsets.

If no entry exists for the current Go version and pointer size (GOARCH is diagnostic only; lookup is by Go version, pointer size, and endianness), the decoder marks every goroutine as unsupported and the endpoint returns `422 unsupported_runtime` before building the object graph.

When the layout is known, for each goroutine record in the heap dump:

1. Read the `runtime.g` address from the goroutine record.
2. Apply the verified field offset to locate the `labels` pointer.
3. Follow the pointer chain through the `runtime/pprof` label map structure to recover key/value pairs.
4. Read string bytes through a composite reader:
   - heap dump object contents first (for heap-allocated label strings),
   - process memory reader second (for string literals in read-only program memory).

Label decoding runs before graph construction. Any label-decode failure makes the match set non-authoritative — an undecodable goroutine might also carry the requested labels, so even a partial match count is not returned as a 200 response. Missing label string bytes return `422 string_missing`. Other structural label recovery failures (e.g. `g_object_missing`, `malformed`) return `422 label_recovery_failed`.

### Step 6 — Build the structural object graph

`snapshotgraph.Build` converts the parsed heap dump into a process-wide directed graph:

- **Nodes**: one per heap object, indexed by `ObjectID`.
- **Edges**: pointer fields within objects pointing to other objects (interior pointers resolved to the owning object).
- **Goroutine roots**: each goroutine's stack frame pointers, grouped by goroutine.
- **Global roots**: data segment, BSS segment, finalizer queues — shared across the entire process.

Reachability is **not** computed here. This step is structural only: O(objects + pointers).

### Step 7 — Select matched goroutines

Walk all non-system goroutines (those not identified as GC workers, finalizer goroutines, g0, etc.). A goroutine is selected if its recovered label set contains **every** key/value pair in the request selector. Extra labels on the goroutine are allowed.

If no goroutines match, the endpoint returns a successful `200` response with `matched_goroutines: 0` and `reachable_bytes: 0`.

### Step 8 — Single BFS from the union of matched roots

`snapshotgraph.ReachableFrom` performs one breadth-first search starting from the union of all matched goroutines' root sets. This is equivalent to computing the transitive closure of "any object reachable from any matched goroutine" in a single pass.

Cost: O(reachable objects + reachable edges), bounded by the subgraph reachable from the selected goroutines, not the total process heap.

### Step 9 — Compute global and system overlap

If there are global roots, `snapshotgraph.ReachableFrom` runs a second time from the global root set. The intersection with the goroutine-reachable set yields `global_overlap_*`.

If system goroutines are excluded (the default), a third BFS runs from system goroutine roots to compute `system_overlap_*`.

These overlaps are reported separately. They are not subtracted from `reachable_bytes` because the endpoint reports reachability from the selected goroutines as a fact, not an exclusive attribution.

### Step 10 — Sum sizes and return JSON

Iterate the reachable object set, summing `Object.Size` (the shallow size of each heap object as reported by the runtime). Return a `200` JSON response.

## Why labels and heap graph come from the same snapshot

`runtime/debug.WriteHeapDump` stops the world. Both `runtime.g.labels` pointers and heap object graphs are serialized at exactly the same instant. There is no time-skew between "which goroutines were running job X" and "what was on the heap at that time."

Earlier prototype approaches used a separately captured `goroutine.pprof` for label recovery. That approach is rejected: `goroutine.pprof` is captured at a different runtime moment and may not match the heap dump, producing incorrect attribution.

Process address space reads (Step 4) access only memory-mapped read-only segments. The string bytes at those addresses are immutable (string literals in Go are immutable), so reading them after the heap dump is consistent.

## Concurrency control

The endpoint uses a mutex to ensure only one heap dump runs at a time. Concurrent callers receive `429 Too Many Requests`.

## Complexity summary

| Step | Cost |
|---|---|
| Layout lookup | O(1) |
| Heap dump capture | O(live heap) — stop-the-world |
| Label decode | O(goroutines × labels) |
| Graph build | O(objects + pointers) |
| BFS from matched roots | O(reachable objects + reachable edges) |
| Overlap BFS | O(global-reachable + system-reachable) |
| Size sum | O(reachable objects) |

The dominant cost in practice is the heap dump capture (stop-the-world, disk I/O).
