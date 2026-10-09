# Object inspection and reference explanations

Use a saved capture to move from bubble totals to particular objects and valid
reference paths. The `objects` and `explain` commands build one graph per
invocation and reuse it for their classifications, distributions, memberships,
and explanations. Existing capture endpoints and bundle formats are unchanged.

## Locate significant objects

```sh
bubblepprof objects capture.tar --labels tenant=atlas-bikes --sort size --limit 20
bubblepprof objects capture.tar --labels tenant=atlas-bikes --by tenant --format json
```

Each listing includes:

- Snapshot-local object ID and hexadecimal allocation address.
- Recorded shallow size and deduplicated outgoing object-reference count.
- `exclusive` or `shared` classification relative to the selected goroutines
  and **all other modeled roots**.
- Whether globals, non-selected system goroutines, or other non-selected
  ordinary goroutines can also reach the object.
- Group memberships when `--by` is specified, with total counts and an explicit
  membership truncation flag.

The selection uses the existing AND semantics. `--label key=value` accepts a
single exact pair, including values containing commas. Goroutines carrying the
same selected labels are combined before reachability is measured.

The listing defaults to descending size, with address and ID breaking ties.
`--sort address` orders by address instead. `--limit 0` explicitly requests every
reachable object; the default is 20. An empty selection has zero matched
goroutines, no objects, and an empty distribution.

Every report also includes full selected-set reachable, exclusive, and shared
object/byte totals and a size distribution. Distribution buckets are inclusive:
`0..0`, `1..1`, `2..3`, `4..7`, and so on through the uint64 size range. Empty
buckets are omitted. Listing limits do not affect totals or the distribution.
This makes large populations of small allocations visible even when individual
large objects dominate the listing. Sizes are shallow, not retained sizes.

Globals include modeled data/bss, other roots, finalizers, and queued finalizers.
`--include-system` allows system/background goroutines into selectors and label
groups. Selected goroutines are excluded from the competing goroutine roots.
Missing grouping labels are not turned into an invented group; their reach is
still represented by the appropriate other-goroutine/system relationships.
Root relationships can overlap each other.

## Explain one object

Take an address from `objects` or `shared`:

```sh
bubblepprof explain capture.tar --labels tenant=atlas-bikes --object 0xc000890000
```

The address can be an allocation address or an interior pointer. The report
resolves it to the containing allocation and reports that allocation's ID,
address, size, classification, and root relationships. A missing allocation is
an analysis error. An existing allocation unreachable from the first selector
is explicitly classified `not_reachable_from_selection`; it may still be
reachable from other selectors or roots.

Each witness starts with an actual modeled root and follows object edges to the
target. The root identifies its category, goroutine ID when applicable, root
kind, pointer, slot address, and available stack function/frame metadata. Object
transitions include the pointer's offset within the preceding allocation and
its raw target pointer when retained. Interior target pointers are preserved.
Offsets are numeric byte offsets; they are not source-level field names.

The search uses multi-root BFS to find one shortest path in object edges for
that source. Root and edge order breaks ties. A witness proves reachability. It
does not prove uniqueness, dominance, or that removing a particular reference
would free the object.

## Explain a shared object from several bubbles

```sh
bubblepprof shared capture.tar --a tenant=atlas-bikes --b tenant=globex
bubblepprof explain capture.tar \
  --a tenant=atlas-bikes --b tenant=globex \
  --object 0xc000890000 --by tenant --max-paths 6
```

`--labels`, `--a`, and `--b` each define an independent AND selector. They are
considered in that order; the first present selector defines object
classification. Each requested selector receives matched-goroutine and target
reachability results, even if it matches no goroutines or cannot reach the
object. `--a-label` and `--b-label` support exact pairs with commas in values.

Paths are attempted first for reachable explicit selections, then for globals,
non-selected system goroutines, other non-selected ordinary goroutines, and
reachable label groups. `--by tenant` discovers all eligible tenant groups that
reach the target, including groups that were not explicitly requested. Group
memberships are computed independently of the number of printed paths.

`reachable_sources` counts these source selections/categories/groups; they may
select overlapping roots. It is not a count of distinct goroutines, objects,
or independent retention paths. `paths_truncated` reports omitted sources.
The report never chooses one reachable bubble as the object's owner.

## Aggregate investigation

```sh
bubblepprof explain capture.tar --labels tenant=atlas-bikes --top 20 --by tenant
```

Aggregate mode reports:

- Largest reachable objects and full-set totals/size distribution.
- Largest shared and largest exclusively reachable objects.
- Root relationships and optional group memberships for listed objects.
- Representative witness explanations chosen round-robin from the three
  rankings, deduplicated by object ID.
- Snapshot validation, warnings, and unresolved-reference diagnostics.

`--top` must be positive and limits each list independently. `--path-objects`
limits the representative objects (default 3; zero also uses 3).
`representatives_truncated` indicates when additional ranked objects were not
explained. Aggregate mode takes one selector. For explicit paths from several
selectors, use single-object mode. `--object` cannot be combined with `--top`
or `--path-objects`.

## Output bounds and analysis cost

| Option | Default | Meaning |
|---|---|---|
| `objects --limit` | 20 | Listed objects; 0 explicitly requests all |
| `explain --top` | 20 | Objects in each aggregate ranking |
| `--group-limit` | 20 | Membership values per object; 0 uses 20 |
| `--max-paths` | 6 | Witness source reports per object; 0 uses 6 |
| `--max-steps` | 64 | Displayed objects per witness; 0 uses 64 |
| `--max-visited` | 0 | BFS visited-object budget; 0 permits at most V |

Negative limits are rejected. Membership totals remain exact when names are
truncated. A found witness records `total_steps` and `truncated`; a truncated
listing shows the root-side prefix and does not pretend to show the entire
route to the target. The target remains identified separately in the object
report. A search that exhausts `--max-visited` reports `search_limit`, which is
not evidence that the object is unreachable. Exact selection/group reachability
is computed separately from this witness-search budget.

Default output is bounded by object, membership, source, and path-step limits.
Graph walks remain proportional to the available graph: each BFS needs O(V)
visited/predecessor storage and examines at most the supplied roots plus V+E.
Object ranking uses a temporary ID list and sorting; group discovery uses
repeated walks, retaining one group set at a time. Aggregate explanations reuse
the graph but perform separate bounded-number path searches. `--max-visited`
limits individual witness searches, not parsing or exact classification.
There are no machine-dependent timing guarantees.

## Provenance and diagnostics

Both commands enable optional reference provenance at load time. The parser
retains non-zero object-pointer slot offsets in parallel with pointer values.
The graph keeps resolved slot references, including duplicate slots targeting
the same object, alongside the compact deduplicated adjacency graph. It retains
available frame metadata for stack roots. No second heap graph or raw object
contents are retained, and dump readers can close after loading.

Internal reusable sessions provide `Objects`, `Explain`, and `ExplainTop`.
Set `memusage.Options.KeepReferenceProvenance` when loading to retain slot/frame
details. Paths still work in a session loaded without it; unavailable transition
offsets/pointers are omitted rather than invented. Metadata exposes
`reference_provenance` so consumers can tell which loading mode was requested.

Use `--format json` for capture metadata, diagnostics, and structured results.
Addresses/pointers are hexadecimal strings to preserve precision in JSON
consumers. IDs and addresses identify objects within this snapshot only.

Label failures affecting eligible goroutines are errors. Unresolved references
and parser diagnostics remain visible; exclusive reachability and paths refer
to the graph actually represented by the snapshot. No concrete type names,
source field names, allocation stacks, dominator trees, or retained sizes are
inferred. Dominator analysis is optional later work, not part of these reports.
