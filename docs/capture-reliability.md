# Capture controls and reliability

Capture continues to use `runtime/debug.WriteHeapDump`; offline analysis needs
no running target, custom runtime, eBPF, or core dump. `Register` shares a single
capture gate between the in-process and bundle endpoints. Separate handler
instances have separate gates; use `RegisterWithOptions` to coordinate both.

```go
bubblepprof.RegisterWithOptions(mux,
    bubblepprof.MemUsageOptions{
        ScratchDir:        "/var/tmp/bubblepprof",
        CaptureCooldown:   30 * time.Second,
        MinFreeBytes:      512 << 20,
        MaxHeapDumpBytes:  2 << 30,
    },
    bubblepprof.BundleOptions{
        ScratchDir:        "/var/tmp/bubblepprof",
        CaptureCooldown:   30 * time.Second,
        MinFreeBytes:      512 << 20,
        MaxHeapDumpBytes:  2 << 30,
        MaxArtifactBytes:  3 << 30,
    },
)
```

Create the scratch directory beforehand with appropriate access permissions.
It defaults to the system temporary directory. Each request creates a private
temporary directory/file and removes it on success, capture failure, size-limit
failure, or cancellation. Cleanup is best effort; a process crash can leave
files behind. Raw captures can contain application data and secrets. Protect
the endpoints and saved artifacts with the controls used for Go pprof.

## Controls

| Setting | Default | Behavior |
|---|---|---|
| CaptureCooldown | 0 | Minimum interval after request completion, including failures |
| ScratchDir | system temporary directory | Directory for heap capture scratch files |
| MinFreeBytes | 64 MiB | Advisory free-space preflight on Linux, macOS, Windows, FreeBSD |
| MaxHeapDumpBytes | 0, unlimited | Reject oversized dump after the runtime finishes writing it |
| MaxArtifactBytes (bundle) | 0, unlimited | Bound the outgoing complete tar stream |

When both handlers are registered together, the larger configured cooldown
applies to their shared gate. Concurrent requests return `429 busy`. During
cooldown they return `429 cooldown` and an integer `Retry-After` header rounded
up to seconds. Invalid negative cooldown/size settings return a clear capture
configuration failure. `GET ?gc=0|1` continues to override GC for a bundle request.

The free-space check uses bytes available to the current user. Space can change
immediately after checking; it is not a reservation or an accurate prediction
of final dump size. The modest default is an early rejection of obviously full
scratch storage. Set a larger minimum for known workloads. On other platforms
the default check is best effort; an explicitly configured minimum requires a
supported free-space implementation.

`WriteHeapDump` cannot be interrupted or bounded while writing through its raw
file descriptor. A heap size limit is therefore a post-capture check, and cannot
prevent that write from temporarily exceeding the limit or filling a disk.
An outgoing artifact limit can abort an already-started response; the handler
aborts the connection so a partial tar is not presented as a complete capture.
The CLI rejects it. Neither a cooldown nor a limit guarantees a maximum pause.

All goroutines stop during the runtime dump. Pause duration depends on heap
contents, size, output performance, and GC settings. Parsing and graph traversal
remain outside the target in external mode. Cancellation is checked between
capture stages and inside analysis record/payload and graph-building loops;
it does not interrupt `WriteHeapDump`, a blocked arbitrary reader, or a sort
already in progress.

## Validated downloads

```sh
bubblepprof fetch http://127.0.0.1:6060 -o capture.tar \
  --timeout 5m --max-bytes 3221225472
```

`--max-bytes 0` is unlimited. Fetch stages into a private file, enforces the
optional byte limit, and checks tar headers and complete end markers, required
members, bundle version, heap signature, segment indexes and lengths, and
transport errors. It replaces the destination only after validation succeeds.
`-o -` also validates before writing bytes to stdout, using a temporary spool.
Invalid downloads preserve existing destination content and remove the spool.
Validation streams payloads without loading the capture into memory or creating
a second extracted heap copy. It checks transport structure; full heap record
and runtime-layout validation happens during analysis.

Offline archive readers retain metadata/profile/segment allocation bounds and
reject duplicate members. Heap parsing bounds strings and memory records and
checks address arithmetic. Malformed headers cannot grow without a bound.
Graph construction checks byte-total overflow and supports cancellation without
returning a partially built session. Inspect warnings and unresolved references
before interpreting results as complete reference evidence.
