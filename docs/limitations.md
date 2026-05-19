# Known Limitations

## Runtime layout dependency

`runtime.g.labels` is a private field of the `runtime.g` struct. Its byte offset depends on the **exact Go version and pointer size** (4 or 8 bytes). `bubblepprof` uses a static table of verified offsets. Because the offset is determined by Go's struct layout rules — not by the specific architecture — all 64-bit little-endian platforms share one entry per Go version, and all 32-bit little-endian platforms share another.

**Current verified support**: go1.24.\*–go1.26.\*, 64-bit little-endian (amd64, arm64) on Linux, macOS, Windows, and FreeBSD; 32-bit little-endian (arm, 386) on Linux and FreeBSD. Experimental (not required in CI) support exists for go1.27-devel (tip) builds; the pre-release offset may change before go1.27.0 ships.

On any other Go version the endpoint returns `422 unsupported_runtime` and does not proceed. Future work requires either manual verification of new Go releases or a DWARF-based layout discovery path.

## Label string recovery

Ordinary pprof label strings created with `pprof.Labels("key", "value")` may have their byte content stored in the executable's read-only data segment rather than in heap objects. The heap dump alone does not contain those bytes.

`bubblepprof` recovers those bytes using an in-process reader:

- **Linux**: `/proc/self/mem`.
- **FreeBSD**: `/proc/self/mem` when procfs is mounted. When procfs is absent (the FreeBSD default), the reader falls back to the on-disk ELF executable. That fallback reads PT_LOAD segments at their static `Vaddr` values, so it is **only correct for non-PIE binaries** — a PIE binary would have a randomized runtime load bias that the ELF fallback does not correct. In practice this means: FreeBSD support requires *either* procfs mounted at `/proc` *or* a non-PIE build; otherwise literal-string labels return `string_missing`.
- **macOS**: current executable Mach-O segments with ASLR slide correction.
- **Windows** (including Wine): current executable PE sections with ASLR slide correction.

When the reader is unavailable or disabled (`DisableProcessMemoryReader=true`), literal-allocated label strings return `string_missing`.

Heap-allocated label strings (e.g., from `strings.Clone("value")`) do not require the process reader and work on all platforms.

## Shallow sizes only

`reachable_bytes` is the **sum of shallow sizes** of heap objects reachable from matched goroutine roots. Each object is counted once. The size is the object's own footprint — not the transitive size of everything it points to.

This is consistent with how `runtime.MemStats` counts objects, but it means that two goroutines sharing a large buffer will each appear to "reach" the full buffer; the overlap is reported in `global_overlap_bytes` / `system_overlap_bytes`, not subtracted from `reachable_bytes`.

## Global and system overlap is not subtracted

Objects reachable from both a matched goroutine and from global roots (data segment, BSS, finalizers) or system goroutines are included in `reachable_bytes` **and** reported in `global_overlap_bytes` / `system_overlap_bytes`. The endpoint does not automatically attribute shared objects to one owner.

Callers that want exclusive attribution must subtract overlap manually or implement a more sophisticated ownership model.

## Stop-the-world cost

`runtime/debug.WriteHeapDump` stops all goroutines for the duration of the heap dump. On large heaps this pause can be seconds. The endpoint is not suitable for frequent polling in production. Use it for on-demand diagnostics, not continuous monitoring.

A single `/debug/memusage` call holds an exclusive lock; concurrent callers receive `429 Too Many Requests`.

## Disk I/O

The heap dump is written to a temporary file in the OS default temp directory. A large heap requires proportional disk space. The file is deleted after parsing, but the deletion is best-effort and may leave a stale file if the process crashes during a request.

## Sensitive data exposure

The endpoint captures a full heap dump, which may contain any data currently in memory: secrets, keys, tokens, tenant data. Protect `/debug/memusage` with the same network-level and authentication controls you apply to `/debug/pprof`.

## ELF reader and offline use

The offline ELF reader (`internal/addrspace.ELFReader`) reads string bytes from executable load segments. It works reliably only for **non-PIE / non-ASLR** binaries: PIE binaries are loaded at a randomized base address at runtime, so static ELF `Vaddr` values do not match the addresses recorded in a heap dump captured from a running process.

The in-process `/debug/memusage` endpoint is not affected by this limitation because it uses the process memory reader, which reads live virtual addresses directly.

## System goroutine classification

System goroutines (GC workers, finalizer goroutine, `g0`, `gsignal`, background scavenger, etc.) are excluded from label matching by default. Classification is heuristic: goroutines with no user-visible pprof labels and whose stack frames start in known runtime packages are treated as system goroutines.

The heuristic may misclassify unusual goroutines. Set `IncludeSystemGoroutines: true` in `MemUsageOptions` to include all goroutines in label matching.

## Interface field records

The heap dump format marks fields as `iface` (non-empty interface) or `eface` (empty interface). `bubblepprof` records these fields and reports skipped counts in warnings, but does not decode them into graph edges. Decoding an interface data word safely requires resolving the dynamic runtime type, which is not available from the heap dump alone without type metadata — guessing could create false roots.

Objects reachable only through such interface slots may be undercounted in `reachable_objects` and `reachable_bytes`. Practically, the GC-visible pointer fields of concrete types held inside interfaces are still emitted in the heap dump and are followed; the limitation applies to the interface slot itself (the data pointer inside the iface/eface header), not to the transitive graph.

## Interior pointer resolution

The heap dump records pointer fields as raw virtual addresses. When a pointer targets the interior of an object (e.g., a pointer to the second element of an array), `bubblepprof` resolves it to the enclosing object. This is correct for GC reachability but means that a single large array is attributed as one unit, not per-element.
