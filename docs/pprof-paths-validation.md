# Reference-path pprof validation and GoLand fixture

This source change adds opt-in `--pprof-mode paths` after v0.3.0. The published
release is unchanged. Summary export remains the default, with its existing JSON
schema and six custom pprof sample types. No graphical heap explorer is included.

## Automated validation

Using Linux Go `go1.27.2-X:nodwarf5`, passed:

```sh
go test -buildmode=pie ./...
GOARCH=386 go test ./...
GOARCH=arm GOARM=7 CGO_ENABLED=0 go test -exec qemu-arm \
  ./pkg/bubblepprof -run '^TestBundleExportPprof$' -count=1 -v
```

The ARMv7 run uses a non-PIE binary under QEMU; ARM PIE and other OS/runtime
combinations remain covered by the existing CI configuration, which was not
changed. No new CI result is claimed for these commits. Gotip was skipped as
requested.

Tests verify exact object identity coverage, all six metric totals, summary
parity, zero-sized objects, sharing with goroutines/global/system roots, cycles,
duplicate and interior pointers, shortest witnesses and deterministic tie
breaking, synthetic locations, preserved selector labels, missing provenance,
empty selections, label decode failures, signed overflow, closed sessions, and
cancellation during traversal, path reconstruction, and profile assembly.

A 50,000-object chain checks that bounded paths skip omitted middles, keep both
ends, and still emit every object. Odd/even depth limits and unlimited paths are
covered without hardware-dependent timing thresholds. CLI tests retain legacy
usage checks and verify invalid combinations and destination preservation.
Real heap captures round-trip both modes through the profile parser and
`go tool pprof` for all three byte metrics. Those subprocesses keep cgo disabled
independently of the outer tests, preserving ARM cross-compiler compatibility.

## Local fixture for import

The existing local `capture.tar` contains the `profiler_load` application with
128 goroutines selected by `role=heap-scan`. Generated:

```sh
go build -o dist/goland/bubblepprof ./cmd/bubblepprof
dist/goland/bubblepprof export capture.tar --labels role=heap-scan \
  --pprof-mode paths -o dist/goland/heap-scan-paths.pb.gz
dist/goland/bubblepprof export capture.tar --labels role=heap-scan \
  --format json -o dist/goland/heap-scan-report.json
```

`dist/` is ignored. The original capture and existing `reachable.pb.gz` are
preserved and untracked; no captured application data is committed or uploaded.
The new `.pb.gz` is about 70 KiB, with 1,877 object samples, 2,015 synthetic
functions, and 128 representative goroutine frames. The deepest displayed
sample has 69 frames: 64 objects, four source/category frames, and one omission
marker. Seventeen samples have abbreviated paths; no objects are omitted.

An independent parse checked every terminal object, object sample uniqueness,
exclusive/shared partitioning, synthetic code locations, and all six totals
against the companion native JSON report:

| Metric | Exact value |
|---|---:|
| reachable_objects | 1,877 |
| reachable_space | 169,361,264 bytes |
| exclusive_objects | 128 |
| exclusive_space | 6,144 bytes |
| shared_objects | 1,749 |
| shared_space | 169,355,120 bytes |

These are observations for this capture, not fixed test thresholds.

## GoLand import checklist

GoLand UI validation is pending the user's import of
`dist/goland/heap-scan-paths.pb.gz`:

1. Open the file as a profiler snapshot and select `reachable_space`.
2. Expand the tree/flame graph through the selected labels, shared/exclusive
   partition, representative goroutine, stack-root function, and object frames.
3. Check that object names expose addresses, `shallow=… bytes`, and available
   `via pointer +0x…` offsets. Parentheses are deliberately avoided around sizes
   because pprof can strip them as function arguments.
4. Switch among the six custom sample types and compare totals with the table.
   An IDE may display rounded units or hide small frames; the profile values are
   exact and all objects remain present.
5. Check an abbreviated branch: its explicit omission marker must remain
   visible between the root-side and target-side object segments.

The captured `heap-scan` role traverses the resident heap. A large shared set
can consequently appear beneath one representative goroutine/root function
(for example `main.main.func6`), even though other goroutines also reach it.
This is deterministic path selection, not an ownership claim. Native `objects`
and `explain` remain the way to investigate additional roots and references.

If the IDE hides the marker or renders these synthetic paths poorly, record
that behavior before changing the profile representation. Automated pprof
compatibility does not establish GoLand UI usability. See
[export semantics and limits](pprof-interop.md#export-reference-paths-for-goland).
