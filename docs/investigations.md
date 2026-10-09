# Investigation workflows

These workflows use the repository's running Go applications, including
receipt queues, maps, channels, interned strings, and chunk rings. Build the CLI
or install it as shown in [getting started](getting-started.md). Run one example
at a time; both default to port 6060. Protect captures as described in
[capture reliability](capture-reliability.md).

## A. A memory-heavy worker

Start the order pipeline in one terminal:

```sh
go run ./examples/order_pipeline -receipt-cache 128
```

In another terminal:

```sh
bubblepprof fetch http://127.0.0.1:6060 -o orders.tar
bubblepprof inspect orders.tar
bubblepprof labels orders.tar --format text
bubblepprof top orders.tar --by role --sort reachable
bubblepprof top orders.tar --by tenant --sort exclusive
bubblepprof objects orders.tar --labels tenant=atlas-bikes --sort size --limit 10
```

The tenant aggregator keeps a bounded ring of recent compressed receipts while
other workers route receipts through channels. Choose a large reported object
address, then explain it:

```sh
bubblepprof explain orders.tar --labels tenant=atlas-bikes --object ADDRESS --by role
```

Replace `ADDRESS` with the object's hexadecimal address from `orders.tar`.
Inspect root categories and pointer offsets, then compare shared and exclusive
classifications. A channel buffer or map backing storage can lead to additional
objects; shallow size is not the transitive size of the object alone. Available
sampled allocation evidence is printed separately from reference paths.

## B. A shared cache

Stop the order example. Start a small log ingester:

```sh
go run ./examples/log_ingest -tenants 2 -streams 1 -shards 1 \
  -ring 8 -chunk-kb 256 -dict-mb 8
```

Each ingester has a chunk ring and references a shared interned dictionary,
which also has a global reference:

```sh
bubblepprof fetch http://127.0.0.1:6060 -o logs.tar
bubblepprof top logs.tar --by tenant --sort shared
bubblepprof compare logs.tar --a tenant=atlas-bikes --b tenant=globex
bubblepprof shared logs.tar --a tenant=atlas-bikes --b tenant=globex --limit 10
bubblepprof exclusive logs.tar --labels tenant=atlas-bikes
```

Choose a shared object address from `logs.tar`:

```sh
bubblepprof explain logs.tar --a tenant=atlas-bikes --b tenant=globex \
  --object ADDRESS --by tenant
```

Look for paths from both tenants and from a global root. Each tenant's reachable
set includes the entire shared dictionary graph; it must not be divided between
tenants or added twice to form a union. Use `compare`'s exact union and
intersection, and `exclusive` for comparison against all other modeled roots.
Runtime scaffolding can contribute additional shared references, so subtracting
global overlap alone is not a general exclusive-memory calculation.

## C. Unexpected growth

The order pipeline's bounded receipt rings fill as orders complete. Capture
once after startup and again after more orders have passed through it.
Start or restart the order example first if the log example is still running:

```sh
bubblepprof fetch http://127.0.0.1:6060 -o before.tar
curl -fsS http://127.0.0.1:6060/stats
# After observing more completed work:
bubblepprof fetch http://127.0.0.1:6060 -o after.tar
bubblepprof diff before.tar after.tar --by tenant
bubblepprof diff before.tar after.tar --labels tenant=atlas-bikes --format json
bubblepprof diff before.tar after.tar --a tenant=atlas-bikes --b tenant=globex
```

Compare build/runtime/architecture and capture settings first.
Check matched goroutine and root counts: a group disappearing differs from a
present group with zero resolved reachable bytes. Look for changes in size
buckets and exclusive versus shared bytes. Pairwise union can stay constant
while sharing increases.

Inspect large objects in `after.tar` and follow their current paths. Do not reuse
an address from `before.tar` as evidence of identity in `after.tar`. Bounded cache
filling can cause expected growth; growth alone is not proof of a leak. A
completed goroutine disappearing also does not prove objects were reclaimed.

## D. Start with ordinary pprof

Both examples expose standard Go profiling endpoints:

```sh
curl -fsS http://127.0.0.1:6060/debug/pprof/heap -o allocations.pb.gz
go tool pprof -top -sample_index=inuse_space allocations.pb.gz
bubblepprof fetch http://127.0.0.1:6060 -o capture.tar
bubblepprof top capture.tar --by tenant --sort reachable
bubblepprof objects capture.tar --labels tenant=atlas-bikes --limit 20 --format json
```

Use sampled allocation hotspots to choose a workload to investigate. Native
object reports expose genuine allocation samples where the runtime dump
provides them, separately from current root/reference paths. An unsampled
object has no inferred allocation origin. These two independently collected
profiles are not synchronized and their totals need not match.

For associated standard profiles in the same bundle, enable
`IncludeHeapProfile`/`IncludeGoroutineProfile` on `BundleOptions`. Each gets its
own timestamp. Then export reachability for standard pprof inspection:

```sh
bubblepprof export capture.tar --labels tenant=atlas-bikes --format pprof -o reachability.pb.gz
go tool pprof -top -sample_index=shared_space reachability.pb.gz
bubblepprof export capture.tar --labels tenant=atlas-bikes --format json -o report.json
```

The exported hierarchy contains synthetic summary frames. It is not an
allocation or execution stack. See [pprof interoperability](pprof-interop.md)
for exact sample types and the tested Pyroscope workflow.
