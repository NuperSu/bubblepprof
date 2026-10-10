# Roadmap completion status

The required features in phases 0–6 are implemented. The existing cross-platform
CI matrix is confirmed passing as of 2026-10-10, following the Linux ARM PIE
pprof export test fix. This confirmation supplements the local validation
records below; their descriptions of what ran locally remain historical records.

## Completed phases

| Phase | Delivered | Validation record |
|---|---|---|
| 0 | Measurement definitions and graph correctness coverage | [Phases 0–1](phase-0-1-validation.md) |
| 1 | Reusable offline sessions, label indexing, metadata, and batch queries | [Phases 0–1](phase-0-1-validation.md) |
| 2 | Group rankings, exact pairwise sharing, exclusive reachability, and shared-object queries | [Phase 2](phase-2-validation.md) |
| 3 | Object inspection, distributions, reference provenance, witness paths, and aggregate explanations | [Phase 3](phase-3-validation.md) |
| 4 | Snapshot comparisons, sharing deltas, structural summaries, and compatibility diagnostics | [Phase 4](phase-4-validation.md) |
| 5 | Standard profile capture, pprof/JSON export, sampled allocation evidence, and Pyroscope interoperability | [Phase 5](phase-5-validation.md) |
| 6 | CLI consolidation, investigation guides, capture safeguards, analyzer robustness, and portable packaging | [Phase 6](phase-6-validation.md) |

Measurements describe reachable, exclusively reachable, and shared objects under
the modeled roots. They do not establish ownership, and totals for separate
selectors are not additive.

## Release publication

[v0.3.0](https://github.com/NuperSu/bubblepprof/releases/tag/v0.3.0) is the first
release of the completed roadmap, following v0.2.2. It provides portable archives
and checksums for 11 targets. The release workflow tests and packages a version
tag, then prepares a draft for review before publication. See [releases](releases.md).

## Optional work within the roadmap

These items are not required for phases 0–6 completion:

- Dominator trees and retained-size analysis (§3.6).
- A built-in regression threshold checker (§4.5).
- Separate Parca validation (§5.4); Pyroscope satisfies the requirement to test
  at least one external profiling backend.
- Advanced sharing queries such as ranking bubble pairs by overlap, finding
  objects shared by at least three groups, and identifying shared subgraphs (§2.5).
- Further multi-source and compressed-membership algorithm optimizations (§2.6).

## Later directions

The roadmap defers richer concrete Go type and field reconstruction, reverse
reference indexes, an interactive HTML explorer, multi-snapshot history, IDE
source links, deeper profiling integrations, compact/external-memory graph
processing, and optional core-dump import. Runtime metadata and concurrent
capture improvements remain research directions.

These are future opportunities rather than unfinished requirements for the
current implementation. See [limitations](limitations.md) for current operational
and analysis boundaries.
