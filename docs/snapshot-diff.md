# Comparing snapshots

Capture two bundles, then compare label groups:

```sh
bubblepprof diff before.tar after.tar --by tenant
bubblepprof diff before.tar after.tar --labels tenant=atlas-bikes
bubblepprof diff before.tar after.tar --a tenant=atlas-bikes --b tenant=globex
bubblepprof diff before.tar after.tar --by tenant --format json > changes.json
```

Inputs are saved bundle files. Each is loaded into a separate reusable analysis
session; its graph is constructed once. Flags may precede or follow paths.
Selectors use AND semantics. Repeat `--label`, `--a-label`, or `--b-label` for
exact key/value pairs, including values containing commas. `--include-system`
lets system/background goroutines participate in selections in both snapshots.

## Measurements

Every selection reports before, after, and after-minus-before changes for:

- Distinct reachable objects and their shallow bytes.
- Exclusive reachability relative to all other modeled goroutines and global roots.
- Shared reachability, including global, other system, and other goroutine overlap.
- Object-size distributions, in inclusive binary size buckets (zero separately).
- Matched goroutine counts, resolved root-reference counts, root categories,
  and stack root function names available in the dump.

Root counts count resolved reference occurrences, including repeated roots to
one object. Root functions describe stack root evidence, not allocation stacks.
Empty functions and unsupported function information are not invented.
Size distributions cover the full selected reachable set, including shared objects.

Group mode compares the sorted union of observed values. `present: false` means
no eligible goroutine matched; `present: true` with zero reachable bytes means a
matched group with no resolved reachable memory. These are different states.
A completely absent grouping key produces an empty group list.

Pair mode additionally compares A-only, B-only, intersection (`shared`), and
union. The union can stay constant while shared memory increases. Pairwise
A-only/B-only sets are distinct from process-wide exclusive reachability.
Group totals and overlap categories must not be summed: they can overlap.

## Compatibility and interpretation

The report starts with compatibility evidence: application build, authoritative
Go version and architecture from the dump, capture settings, completeness
indicators, and selected goroutine/root counts. Known differences are reported;
they do not prevent investigation. Architecture and build differences can
explain byte changes. Diagnostics, unresolved pointers, and read-only segment
status should be reviewed before treating differences as regressions.

New version-1 bundles add optional application build information (including
available build settings) and GC/read-only capture settings. Older bundles
remain readable and report those checks as `unknown`. Equal completeness
indicators do not prove a complete model of every runtime root.

Snapshots are compared by selectors and aggregate structure. Objects are never
matched across captures by addresses, IDs, or size. Growth does not prove a
leak. A goroutine disappearing changes reachability without proving that
objects were reclaimed. Subsequent object investigation must use an address
from the particular snapshot being investigated.

## JSON contract

`schema_version: 1` has `selection`, `compatibility`, both snapshot metadata
records, `groups`, optional `pair`, and interpretation `notes`. Each group has
its selector, before/after summaries, metric deltas, size bucket deltas, root
category/function deltas, and goroutine/root deltas. Pair data retains both
comparisons and their metric deltas.

Byte deltas are signed decimal **strings**, so the entire unsigned 64-bit byte
range can be subtracted exactly, including values outside signed 64-bit range.
Consumers should parse them as arbitrary-precision integers, not floating point.
Object and root deltas are signed integers. Before/after measures retain the
existing unsigned byte and integer object count representation. Missing buckets
or root categories are treated as zero when computing deltas. Output is
suitable for caller-defined regression checks; no threshold policy is imposed.
