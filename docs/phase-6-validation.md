# Phase 6 validation

## CI confirmation

The existing cross-platform CI matrix was confirmed passing on 2026-10-10 after
the Linux ARM PIE pprof export test fix. Go 1.25+ can build `pprof` on demand for
the host while inheriting the target's cross compiler. The export integration
test now disables cgo only for that subprocess, preserving the outer PIE tests.
Regression coverage supplies an unusable C compiler and checks all three byte
sample types.

Local follow-up validation passed `go test -buildmode=pie ./...`,
`GOARCH=386 go test ./...`, and the ARMv7 export integration test under QEMU.
The local ARM run used a non-PIE binary because the cross C compiler was not
installed; the ARM PIE configuration is covered by the confirmed CI run.
See [project status](project-status.md) for completion and release steps.

## Implementation

- `inspect` shows capture/build/settings and analysis diagnostics.
- `query` and `labels` preserve `memusage`/`bubbles`; label listings support text
  and JSON. Command help exits successfully; `version` identifies packaged builds.
- Shared endpoint gate supports configurable cooldown and `Retry-After`.
- Shared runtime capture supports private scratch directories, advisory available
  disk-space checks, post-capture heap size limits, and outgoing bundle limits.
- Fetch privately stages and validates archives before replacing destinations or
  writing stdout, with an optional total download byte limit.
- Parsing checks cancellation during records/payload reads and bounds malformed
  signatures; graph construction checks cancellation and byte-total overflow.
- Archive readers reject non-regular required members and trailing metadata JSON.
- Release packaging creates portable CLI archives and SHA256 checksums. A separate
  release workflow tests native platforms and prepares draft releases on version tags.
- Getting-started, operational, and real-application investigation guides cover
  worker memory, shared caches, growth, and standard pprof interoperability.

## Linux suites

Using `go1.27.2-X:nodwarf5`, both full suites passed:

```sh
go test ./...
GOARCH=386 go test ./...
```

Existing runtime integrations, parser corruption/truncation, graph identities,
sharing/cycles, platform readers, and CLI workflow tests remain enabled. Added
coverage includes deterministic cooldown, shared gates across endpoints, scratch
cleanup on success/failure, impossible free-space minimums, heap/artifact limits,
invalid and oversized downloads, complete tar trailers, atomic destination
preservation, stdout staging, mid-parse and mid-build cancellation, object/root
arithmetic overflow, aliases, metadata inspection, clean help, and version output.

## Real application workflow

The packaged Linux amd64 CLI was exercised against the real `log_ingest` example:

```sh
/tmp/bubble-phase6-log-ingest -addr 127.0.0.1:16060 \
  -tenants 2 -streams 1 -shards 1 -ring 4 -chunk-kb 64 -dict-mb 2 -duration 2m
```

Successfully ran `fetch`, `inspect`, `labels`, `query`, `top`, `compare`,
`exclusive`, `shared`, `objects`, pairwise `explain` using an actual shared
object address, `diff` on two captures, `explore` with two tenant queries,
`export`, and `go tool pprof -sample_index=shared_space`.
The observed pairwise intersection contained 1,197 objects and 2,489,784 shallow
bytes. These observations are not performance or byte thresholds: runtime
scaffolding can change them. The test used dictionaries, log chunks, maps,
channels, and running ingesters, not only isolated byte arrays.

## Portable packaging

```sh
bash -n scripts/build-releases.sh
bash scripts/build-releases.sh phase6-dev
```

Built all 11 targets: Linux amd64/arm64/386/armv7; macOS amd64/arm64;
Windows amd64/arm64/386; FreeBSD amd64/arm64. All archive checksums verified,
and the extracted native CLI reported its embedded `phase6-dev` version.
Archives include the license, README, and documentation.

Focused free-space helper test binaries were also compiled for macOS amd64/arm64,
Windows amd64/arm64/386, and FreeBSD amd64/arm64. Runtime behavior on those OSes
remains covered by native GitHub Actions; cross-compilation is not a claim of
local runtime validation. The release workflow YAML structure was parsed locally.
Existing CI version/architecture coverage was preserved.

Gotip was skipped as requested. No release tag was created or release published;
the generated archives are local and the workflow prepares reviewable drafts.

## Practical limits

The runtime's stop-the-world write cannot be interrupted or constrained through
its raw descriptor. Heap limits apply after writing; free-space preflights are
advisory, not reservations. Files can remain after a process crash. Cancellation
does not interrupt a blocked arbitrary reader or a sort in progress. Download
validation checks transport/archive structure; runtime layout and heap record
validation still belong to analysis. These boundaries are documented in
[capture reliability](capture-reliability.md).
