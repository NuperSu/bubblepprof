# Phase 2 validation

Validated on Linux with Go `go1.27.2-X:nodwarf5`, 2026-10-10.

Passed:

- `go test ./...` (native amd64)
- `GOARCH=386 go test ./...`
- `git diff --check`

Gotip was omitted as requested. Windows and macOS were not run locally; the
existing CI coverage remains unchanged. The new analysis code and CLI use no
platform-specific dependencies or changes to capture/runtime layouts.

## Correctness coverage

Synthetic graphs exercise exact reachable sets and object identities, pairwise
differences/intersection/union, duplicate roots, cycles, three-group sharing,
global/system/ungrouped overlaps, missing and empty label values, included system
groups, overlapping selectors, absent matches, sorting, display limits,
cancellation, closed sessions, label-decoding failures, malformed IDs, and
checked uint64 byte accumulation.

A generated 3,000-group graph checks exact per-group reachable, exclusive, and
shared totals without hardware-dependent timing assertions.

A real heap capture is loaded into a reusable session and analyzed through all
four new CLI commands. JSON results match the session results exactly; text
output and diagnostics are exercised too. Exclusive reachable totals also match
the existing single-selector query on that same capture. Existing capture,
parser, examples, label recovery, and offline integration tests pass on both
Linux architectures.

## Scope and limits

Ranking uses repeated exact graph walks with per-object membership counts;
selector sets are not cached. Pairwise object listings materialize/sort the
intersection before applying a display limit. These costs are documented in
[multi-bubble analysis](multi-bubble-analysis.md).

Exclusivity is relative to the roots and references represented by the snapshot.
Parser and unresolved-reference diagnostics remain exposed. Group totals are
not additive. No object ownership or allocation attribution is inferred.
Object paths, type reconstruction, and snapshot diffs remain later phases.
