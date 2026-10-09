# Phase 3 validation

Validated on Linux with Go `go1.27.2-X:nodwarf5`, 2026-10-10.

Passed:

- `go test ./...` (native amd64)
- `GOARCH=386 go test ./...`
- `git diff --check`

Gotip was omitted per the user's request. Windows and macOS were not run
locally; existing CI coverage remains unchanged. The new parser provenance,
graph search, analysis, and CLI code uses platform-independent Go.

## Regression coverage

Parser and graph tests cover optional provenance, duplicate pointer slots,
nil fields preceding live pointers, interface data-word offsets, 4/8-byte
pointers, checked offset/slot arithmetic, interior target pointers, frame
metadata, and unchanged compact adjacency without provenance.

Witness tests check exact paths and every reported transition, multiple roots,
shortest BFS discovery, cycles, direct-root targets, invalid roots/children,
absent objects, cancellation, missing provenance, and explicit search exhaustion.
An iterative 50,000-object chain checks large-path correctness and visit limits.

Inspection tests check exact object IDs/classifications, global/system/ordinary
root overlaps, size and address ordering, zero-sized objects, full distributions
under listing limits, shared paths from two groups, exclusive objects, empty
selections, invalid options, label failures, closed sessions, and byte overflow.
A generated 2,000-group graph verifies exact membership totals with bounded
names and witness output. Aggregate tests cover independent ranking limits,
representative sampling, and very large requested limits on empty selections.

A real runtime heap capture contains a cyclic structure with a shared byte
allocation reached by two labeled goroutines and a global root. Integration
coverage verifies both goroutine witness paths, global reachability, recovered
group memberships, and retained pointer offsets. JSON results from `objects`,
single-object `explain`, and aggregate `explain` match the reusable session;
text output and diagnostics are also checked. Explanations remain identical
after the saved capture is deleted, confirming no lazy dump reads are needed.
The full existing capture, parser, label, CLI, and example suites pass on both
Linux architectures.

## Completion scope

Phase 3 provides object inspection, distributions, optional reference
provenance, BFS witnesses, shared-object explanations from multiple groups,
and bounded aggregate reports. Capture endpoints, bundles, and existing query
results remain compatible. The optional dominator/retained-size mode was not
implemented. Concrete type/source-field reconstruction and snapshot diffs are
outside this phase.
