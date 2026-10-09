# pprof and Pyroscope interoperability

## Capture ordinary profiles alongside a heap dump

Optional standard profiles are enabled through the existing bundle handler:

```go
bubblepprof.RegisterBundleWithOptions(mux, bubblepprof.BundleOptions{
    IncludeHeapProfile:      true,
    IncludeGoroutineProfile: true,
})
```

The version-1 tar optionally includes `profiles/heap.pb.gz` and
`profiles/goroutine.pb.gz`. Extract them with an ordinary tar reader and use:

```sh
tar -xf capture.tar profiles/heap.pb.gz profiles/goroutine.pb.gz
go tool pprof -top profiles/heap.pb.gz
go tool pprof -top profiles/goroutine.pb.gz
```

Both options default to false, preserving capture cost and existing defaults.
Capture stays independent from analysis. The heap dump is collected first,
then each optional profile. Metadata records the heap dump's start/end and each
profile's separate collection interval, plus available application build data.
These are associated observations, not perfectly synchronized views.

The standard heap profile describes sampled allocations; its weighted values
are not the complete object-reference graph. The goroutine profile describes
execution stacks. Reachability remains based on `debug.WriteHeapDump` records.

Internal bundle writers can associate existing profile bytes through
`WriteInput.Profiles` and supply known collection intervals in `Meta.Profiles`.
An absent timestamp stays unknown. Readers preserve these members, reject
duplicates, and bound their aggregate compressed size to 64 MiB by default
(`ReaderOptions.MaxProfileBytes`). Older bundles and readers remain compatible;
no new member is required. A standard tar reader can extract the profiles
without loading the analysis graph.

## Export reachability

```sh
bubblepprof export capture.tar --labels tenant=atlas-bikes --format pprof -o reachable.pb.gz
go tool pprof -top -sample_index=reachable_space reachable.pb.gz
go tool pprof -top -sample_index=exclusive_space reachable.pb.gz
go tool pprof -top -sample_index=shared_space reachable.pb.gz
```

Repeat `--label key=value` for exact selector pairs, including comma-containing
values. All pairs must match. `--include-system` applies the usual selection
policy. Export reads a saved bundle; `-o -` writes to stdout.

The gzip-compressed profile has six custom sample types:

| Type | Unit | Meaning |
|---|---|---|
| `reachable_objects` | count | Distinct selected reachable objects |
| `reachable_space` | bytes | Their recorded shallow sizes |
| `exclusive_objects` | count | Objects unreachable from all other modeled roots |
| `exclusive_space` | bytes | Their shallow sizes |
| `shared_objects` | count | Objects also reachable from other modeled roots |
| `shared_space` | bytes | Their shallow sizes |

Two disjoint aggregate samples represent exclusive and shared objects. Each
object contributes once to the reachable total and once to its partition.
Zero-object partitions are omitted. A zero-sized object still contributes to
object counts. Results for separate selectors are not additive.

The hierarchy contains only explicitly named `[bubblepprof summary]` frames:
selected reachable set, then exclusive/shared reachability. These are synthetic
summary categories, **not allocation stacks or execution stacks**. No object
address is turned into a code location, no reference path is disguised as a
call stack, and a shared object is not duplicated for every root reaching it.
Flamegraph width means reachable shallow bytes (or the chosen custom metric),
not allocation rate or dominator retained size. Choose the sample type explicitly.
The native `objects` and `explain` commands retain richer graph evidence.

Original selector labels are attached to samples. The profile timestamp uses
the dump capture start when available, otherwise the bundle creation timestamp.
Byte totals beyond signed int64 are rejected explicitly because pprof uses
signed sample values; native JSON supports uint64 bytes.

## Inspect genuine allocation evidence

```sh
bubblepprof objects capture.tar --labels tenant=atlas-bikes --limit 20 --format json
bubblepprof explain capture.tar --labels tenant=atlas-bikes --object 0xc000890000
```

The dump's memory-profile buckets and allocation-sample records are preserved.
Objects with associated samples expose `allocation_samples` separately from
reference paths, in JSON and text output. Evidence includes sample and bucket
addresses, an offset within the object, sampled allocation size, allocation
frames (function/file/line), and bucket allocation/free counters.

Interior samples can occur in combined tiny-allocation objects. Their offsets
remain explicit; the sample does not describe every allocation in that object.
Bucket counters apply to the bucket, not a particular object's lifetime.
Objects without an associated sample receive no invented allocation origin.
Missing buckets, ambiguous duplicate buckets, and samples outside known objects
produce diagnostics. These associations are confined to one snapshot.

The synthetic reachability export intentionally excludes allocation frames.
Use an ordinary heap profile to study sampled allocations, and native object
inspection to relate available sampled origins to current reference evidence.

## Portable JSON report

```sh
bubblepprof export capture.tar --labels tenant=atlas-bikes --format json -o report.json
```

Schema version 1 includes `capture`, `schema_version`, `labels`, `metadata`,
`summary`, and `semantics`. The summary contains presence, matched goroutine and
resolved root counts, all reachable/exclusive/shared/overlap measures, the full
size distribution, and root category/function counts. Counts and bytes are
exact integers; consumers should preserve uint64 precision. Capture and parser
metadata retain diagnostics. It is an aggregate interchange report, not a
replacement for the native reference graph. The versioned snapshot `diff` JSON
remains available for regression checks.

## Pyroscope smoke check

Custom sample types can be ingested using Pyroscope's multipart
[`/ingest` API and custom sample configuration](https://grafana.com/docs/pyroscope/latest/reference-server-api/).
The repository includes a standard-library-only Python check:

```sh
mkdir -p /tmp/bubblepprof-interop
BUBBLEPPROF_EXPORT_FIXTURE_DIR=/tmp/bubblepprof-interop \
  go test ./pkg/bubblepprof -run '^TestBundleExportPprof$' -count=1
python3 scripts/check_pyroscope.py \
  --url http://127.0.0.1:4040 \
  --profile /tmp/bubblepprof-interop/reachability.pb.gz \
  --report /tmp/bubblepprof-interop/report.json
```

Run against a disposable local Pyroscope instance. The check creates a unique
service, ingests the profile with all six custom sample definitions, and queries
all six back. It verifies original selector label filtering, exact native
totals, and exclusively synthetic summary names in the flamegraph. Generate
the fixture immediately before running it; the query window covers 30 minutes.

Multipart requests omit the `format` query parameter so the server recognizes
the multipart profile and sample configuration. The sample configuration marks
these values as unsampled snapshots and uses `average` aggregation. Do not
interpret sums across capture times as total reachable memory. In Pyroscope,
select the custom metric and a single capture/time window to examine it; select
`reachable_space`, `exclusive_space`, or `shared_space` as appropriate.

Tested with standalone Pyroscope **v2.4.0**, legacy (`v1`) storage and ingester
write path. No Grafana plugin or backend dependency is required for ordinary
bubblepprof use. See [phase 5 validation](phase-5-validation.md) for the tested
backend configuration and results. Parca is not separately validated in this phase.
