# Multi-bubble analysis

Capture once, then investigate multiple label groups using the same heap
snapshot. These commands accept a bundle file or a capture endpoint URL.
Existing `memusage`, `bubbles`, and `explore` commands retain their behavior.

## Rank bubbles

```sh
bubblepprof top capture.tar --by tenant
bubblepprof top capture.tar --by tenant --sort exclusive --limit 20
bubblepprof top capture.tar --by tenant --sort shared --format json
```

Each distinct value of `tenant` defines one group of goroutines. Goroutines
can carry other labels; all goroutines with the same tenant are grouped together.
An empty label value is a valid named group. Missing labels are counted separately
as `ungrouped_goroutines` and their roots participate in overlap analysis.
System/background goroutines are excluded from groups by default and counted
as `excluded_system_goroutines`. `--include-system` allows them to join groups.

Each object's recorded shallow size is counted once per reachable set. Group
rows are sorted by descending bytes, with label values breaking ties. Limiting
rows does not exclude hidden groups from sharing calculations. `--limit 0`
shows all groups. Row totals are not additive: multiple rows can reach the same
object. These totals are not total distinct process memory.

For group i, let Ri be its reachable objects, and O the union reachable from
globals, system goroutines outside named groups, and goroutines missing the
selected label. The four disjoint categories partition Ri:

| JSON category | Meaning |
|---|---|
| `exclusive` | Reached by i and no other named group or outside root |
| `bubbles_only` | Reached by other named groups, but no outside root |
| `other_roots_only` | Reached by outside roots, but no other named group |
| `both` | Reached by other named groups and outside roots |

`shared` is the union of the latter three categories. `bubble_overlap` combines
`bubbles_only` and `both`; `other_root_overlap` combines `other_roots_only` and
`both`. These two overlap measures can overlap and must not be added.

Each category reports both `objects` and `bytes`. Root details include
`global_overlap`, `system_overlap`, and `ungrouped_overlap`. Globals use all
process-wide roots modeled by the existing graph: data, bss, other roots,
finalizers, and queued finalizers. System overlap includes only system goroutines
outside named groups. With `--include-system`, a system goroutine missing the
label contributes to both system and ungrouped overlap. Root-category overlaps
can overlap each other.

## Compare two selections

```sh
bubblepprof compare capture.tar --a tenant=atlas-bikes --b tenant=globex
bubblepprof compare capture.tar --a tenant=atlas-bikes,role=ingester --b role=ingester --format json
```

Selectors use the existing AND behavior. Reported sets are A-only relative to B,
B-only relative to A, their exact intersection (`shared`), and their union. Each
set reports objects and bytes. `overlapping_goroutines` explicitly identifies
roots selected by both selectors. Identical or overlapping selections are valid.
No matching goroutines produces empty sets and a zero matched-root count.

Pairwise differences are not exclusive reachability relative to the rest of the
process: an A-only object can still be reached by globals or a third bubble.
Use repeatable `--a-label key=value` and `--b-label key=value` when values contain
commas. `--a` and `--b` also accept repeated comma-separated selectors.

## Query exclusive reachability

```sh
bubblepprof exclusive capture.tar --labels tenant=atlas-bikes
bubblepprof exclusive capture.tar --label tenant=atlas-bikes --label role=ingester --format json
```

This compares the selected goroutines with **all other modeled roots**: globals
and every non-selected goroutine, including unlabeled and system goroutines.
It reports reachable, exclusive, and shared sets, plus global, non-selected
system, and other ordinary goroutine overlaps. Selected system goroutines are
not counted as competing roots when `--include-system` is used.

Exclusive reachability describes this snapshot's modeled graph. It does not
establish ownership or prove what removing one reference would reclaim.

## Inspect shared objects

```sh
bubblepprof shared capture.tar --a tenant=atlas-bikes --b tenant=globex
bubblepprof shared capture.tar --a tenant=atlas-bikes --b tenant=globex --limit 0 --format json
```

The shared command returns the exact pairwise intersection with object IDs,
hexadecimal addresses, recorded shallow sizes, and outgoing object reference
counts. Objects are sorted by descending size, then address and ID. The default
limit is 20; zero returns the entire set. `truncated` distinguishes a limited
listing from a complete one. Aggregate intersection counts always describe the
full set, even when the listing is limited. IDs and addresses identify objects
only within this snapshot. Reference paths and type reconstruction are later
roadmap features.

## Sessions, diagnostics, and cost

The internal snapshot API exposes `Top`, `Compare`, `Exclusive`, and `Shared`.
They reuse the graph loaded by phase 1 and retain no selector-result caches.
Ranking walks each group twice and stores one membership counter per reached
object, rather than retaining an object set for every group. Work grows with the
sum of group traversals; this first implementation deliberately uses simple,
exact graph walks. Shared listings materialize and sort the full intersection
before limiting output. Use a file capture for repeated CLI invocations to avoid
recapturing the application.

JSON reports include capture identity and metadata/diagnostics alongside
`result`. Text reports include validation state, unresolved-pointer counts, and
warnings. Label decoding failures affecting eligible selections are errors,
not successful zero-match reports. Unresolved graph references remain visible
in diagnostics; exclusive means exclusive within the available modeled roots.
No capture endpoint, bundle format, or existing API response changes are needed.
