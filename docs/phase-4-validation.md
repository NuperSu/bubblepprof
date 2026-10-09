# Phase 4 validation

Implemented snapshot comparisons by grouping label, one AND selector, or two
selectors for pairwise sharing. Reports preserve shared and exclusive metrics,
structural evidence, presence versus zero-memory states, compatibility evidence,
and exact signed JSON byte deltas. No object identity is inferred across captures.

Validation on Linux using `go1.27.2-X:nodwarf5`:

```sh
go test ./...
GOARCH=386 go test ./...
```

Both full suites passed, including real heap capture and CLI integration tests.
Gotip was skipped as requested. No OS-specific analysis behavior was introduced.

Focused coverage includes:

- Exact reachable/shared/exclusive sets with cycles, multiple roots, and globals.
- Global, system, and other goroutine overlap summaries.
- Constant pairwise union while intersection increases.
- Missing groups versus matched goroutines with zero resolved reachable memory.
- Size distribution, root category, and stack function summary changes.
- Positive and negative byte differences spanning the full uint64 range.
- Live before/after bundles with disappearing and appearing labeled goroutines.
- Group, selector, and pair CLI JSON modes plus text mode and invalid usage.
- Additive capture metadata and unknown compatibility checks for older bundles.
- Cancellation and closed summary sessions.

Known limits: summaries compare aggregate evidence, not object lifetimes.
Completeness diagnostics do not prove all runtime roots are modeled; build,
architecture, capture settings, and label/root changes can explain deltas.
See [snapshot comparisons](snapshot-diff.md) for the schema and interpretation.
