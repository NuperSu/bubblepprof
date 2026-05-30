# `log_ingest` — multi-tenant log-ingestion demo

A runnable showcase for [`bubblepprof`](../../README.md). It models a multi-tenant
in-memory log ingester (Loki / Cortex / Mimir flavor) — the canonical system where the
operational question is literally *"how much heap is tenant X holding right now?"*, which
is exactly what `POST /debug/memusage` answers.

## What it does

One long-lived **ingester goroutine per `(tenant, stream, shard)`**. Each ingester
**privately** owns a ring of in-memory log chunks, held only on its own goroutine stack —
no chunk is shared between workloads. Every ingester also references **one** process-wide
interned-label dictionary, which is the *only* shared memory in the program and therefore
the only source of `global_overlap`.

That split is the whole point:

```
reachable_bytes       = this selector's private chunks + the shared dictionary
global_overlap_bytes  = the shared dictionary (reachable from a package-level global)
reachable - overlap   = this selector's TRULY-PRIVATE heap
```

As you add labels the match narrows and `reachable_bytes` shrinks with it, while
`global_overlap_bytes` (the dictionary) stays roughly constant.

## Run it

```bash
# From the repo root. Default: ~736 MiB live
#   24 ingesters * 24 MiB private ring + 160 MiB shared dictionary
go run ./examples/log_ingest

# Heavier load:
go run ./examples/log_ingest -tenants 8 -shards 4 -dict-mb 256

# Bounded run (exits after 2 minutes):
go run ./examples/log_ingest -duration 2m
```

On start it prints a one-line summary and then heap stats every 2 seconds:

```
pid=4171832 ingesters=24 ring=48 chunk=512KiB private~=576MiB dict=160MiB gomaxprocs=16 addr=127.0.0.1:6060
goroutines=30 heap_alloc=731MiB heap_sys=782MiB chunks_per_sec=210 bytes_alloc=...MiB
```

The endpoint is at `POST http://127.0.0.1:6060/debug/memusage`. The standard
`runtime/pprof` endpoints are also mounted at `/debug/pprof` (bubblepprof augments pprof,
it does not replace it):

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:6060` | HTTP listen address for `/debug/memusage` and `/debug/pprof` |
| `-tenants` | `4` | number of tenants |
| `-streams` | `3` | log streams per tenant |
| `-shards` | `2` | shards per `(tenant,stream)` — one goroutine each |
| `-chunk-kb` | `512` | size of each in-memory log chunk, in KiB |
| `-ring` | `48` | chunks retained per ingester |
| `-dict-mb` | `160` | shared interned-dictionary size, in MiB |
| `-duration` | `0` | how long to run; `0` means until Ctrl+C / SIGTERM |

Ingester count = `tenants × streams × shards`. Per-ingester private heap ≈ `ring × chunk-kb`.
Total private ≈ `ingesters × ring × chunk-kb`; total live ≈ that plus `dict-mb`.

## Labels

Each ingester goroutine is stamped once at start with a six-dimension label set (standard
`runtime/pprof` labels — no bubblepprof wrapper). Selectors use **AND** semantics: a
goroutine matches only if its labels contain *every* key/value you send.

| Label | Values (defaults) | Notes |
|---|---|---|
| `service` | `log-ingester` | constant; also on the `http` and `reporter` goroutines |
| `tenant` | `atlas-bikes`, `globex`, `initech`, `umbrella-corp` | extra tenants are `tenant-04`, `tenant-05`, … |
| `stream` | `app`, `nginx`, `kernel`, `audit` | extra streams are `stream-04`, … (default `-streams 3` uses the first three) |
| `region` | `us-east`, `eu-west`, `ap-south` | assigned per tenant (see mapping below) |
| `tier` | `enterprise`, `standard` | assigned per tenant (see mapping below) |
| `shard` | `0` .. `shards-1` | string form, e.g. `"0"` |

Two infrastructure goroutines carry `service=log-ingester` plus a `role`:

| Label | Values |
|---|---|
| `role` | `http` (the HTTP server), `reporter` (the 2-second stats logger) |

### Default tenant → region / tier mapping

`region` is `regions[tenantIndex % 3]` and `tier` is `tiers[tenantIndex % 2]`, so with the
default four tenants:

| tenant | region | tier |
|---|---|---|
| `atlas-bikes` | `us-east` | `enterprise` |
| `globex` | `eu-west` | `standard` |
| `initech` | `ap-south` | `enterprise` |
| `umbrella-corp` | `us-east` | `standard` |

So `{"region":"us-east"}` matches `atlas-bikes` + `umbrella-corp`, and
`{"tier":"enterprise"}` matches `atlas-bikes` + `initech`.

## Querying

```bash
q() { curl -s -XPOST 127.0.0.1:6060/debug/memusage -H 'Content-Type: application/json' -d "$1" | jq .; }

# Everything tenant=atlas-bikes holds (all streams and shards):
q '{"labels":{"tenant":"atlas-bikes"}}'

# Drill down — reachable falls, global_overlap (the dictionary) holds steady:
q '{"labels":{"tenant":"atlas-bikes","stream":"app"}}'
q '{"labels":{"tenant":"atlas-bikes","stream":"app","shard":"0"}}'

# Cross-cutting views that ignore the tenant axis:
q '{"labels":{"region":"eu-west"}}'
q '{"labels":{"tier":"enterprise"}}'

# Everything: global_overlap ~= dictionary size, reachable ~= all chunks + dictionary:
q '{"labels":{"service":"log-ingester"}}'

# A label that matches nothing returns zero matches, not an error:
q '{"labels":{"tenant":"nonexistent"}}'
```

### Reading the response

```json
{
  "labels": {"tenant": "atlas-bikes"},
  "matched_goroutines": 6,
  "reachable_objects": 12345,
  "reachable_bytes": 192937984,
  "global_overlap_bytes": 167772160,
  "system_overlap_bytes": 0
}
```

| Field | In this demo it means |
|---|---|
| `matched_goroutines` | how many ingesters matched (`streams × shards` for a single tenant) |
| `reachable_bytes` | matched ingesters' private chunks **plus** the shared dictionary |
| `global_overlap_bytes` | the shared dictionary (constant across tenant/stream/shard selectors) |
| **private heap** | compute it yourself: `reachable_bytes − global_overlap_bytes` |

Example progression from a small run (`-dict-mb 32 -ring 8 -chunk-kb 256`), shown as
`matched / reachable / global_overlap / private` in MiB:

```
{tenant:atlas-bikes}              -> 4 / 41 / 33 /  8
{tenant:atlas-bikes,stream:app}   -> 2 / 37 / 33 /  4
{...,stream:app,shard:0}          -> 1 / 35 / 33 /  2
{tier:enterprise}                 -> 8 / 49 / 33 / 16
{service:log-ingester}            -> all / 57 / 33 / 24
```

## Why the numbers are trustworthy

The example deliberately:

- uses **only concrete types** on the data path — bubblepprof does not decode `iface`/`eface`
  pointer fields into graph edges, so interface-only references would be under-counted;
- sets **no `runtime.SetFinalizer`** on chunks — a finalizer registers its object as a global
  root, which would wrongly pull private chunks into `global_overlap`;
- keeps each chunk ring reachable **only** from its ingester goroutine's stack, never from a
  shared registry.

Those three choices are what make `reachable_bytes − global_overlap_bytes` a faithful measure
of per-selector private heap.

> Note: `matched_goroutines` for the broad `{"service":"log-ingester"}` query can exceed the
> ingester count — `net/http` spawns per-connection handler goroutines from inside the labeled
> accept loop, so they inherit `role=http`. They hold negligible bytes and do not affect the
> attribution.

## Security

`/debug/memusage` is as sensitive as `/debug/pprof`: a single call performs a stop-the-world
heap dump and can expose label values and heap sizes. Protect it with the same controls and do
not expose it to untrusted callers.
