# Getting started

bubblepprof analyzes Go heap objects reachable from labeled goroutine groups.
A group is often called a bubble. Use the external analyzer to capture once
and ask several questions without stopping the target for every query.

## Install and register

Install the CLI with a supported Go toolchain:

```sh
go install github.com/NuperSu/bubblepprof/cmd/bubblepprof@latest
bubblepprof version
```

Portable archives and checksums are produced by the
[release workflow](releases.md). For an application:

```sh
go get github.com/NuperSu/bubblepprof@latest
```

Register endpoints on the application's existing diagnostic HTTP server:

```go
import (
    "net/http"
    "github.com/NuperSu/bubblepprof/pkg/bubblepprof"
)

mux := http.NewServeMux()
bubblepprof.Register(mux)
// Serve mux on a protected diagnostic listener, for example 127.0.0.1:6060.
```

Use standard pprof labels. Within a request, `pprof.Do` applies labels for the
callback and restores the previous set when it returns:

```go
pprof.Do(ctx, pprof.Labels("tenant", tenantID, "role", "worker"), func(ctx context.Context) {
    processJob(ctx)
})
```

New goroutines inherit their creator's current labels. A long-lived worker
that receives a new context should explicitly update its goroutine labels:

```go
go func() {
    ctx := pprof.WithLabels(parent, pprof.Labels("tenant", tenantID, "role", "worker"))
    pprof.SetGoroutineLabels(ctx)
    processJobs(ctx)
}()
```

Labels stored only on a context, without `pprof.Do` or
`pprof.SetGoroutineLabels`, do not label the running goroutine. Label selectors
use AND semantics. Repeated `--label key=value` accepts exact values containing
commas. A missing group returns zero matches; label recovery failures are
reported as errors.

## Capture once, investigate offline

```sh
bubblepprof fetch http://127.0.0.1:6060 -o capture.tar
bubblepprof inspect capture.tar
bubblepprof labels capture.tar --format text
bubblepprof top capture.tar --by tenant --sort reachable
bubblepprof query capture.tar --labels tenant=atlas-bikes
bubblepprof exclusive capture.tar --labels tenant=atlas-bikes
bubblepprof objects capture.tar --labels tenant=atlas-bikes --limit 20
```

Choose an object address printed by `objects` from this same capture:

```sh
bubblepprof explain capture.tar --labels tenant=atlas-bikes --object 0xc000890000
```

The address above is illustrative; substitute a reported address. A witness
path shows one chain from a modeled root to the object. It is not an allocation
stack, and removing one reference need not make the object unreachable.

`query` preserves the existing `memusage` command and JSON default. `labels`
preserves `bubbles` and its JSON default. `inspect`, `top`, `compare`, `shared`,
`exclusive`, `objects`, `explain`, and `diff` offer text or JSON. `explore`
evaluates a JSON batch with one graph; `export` writes pprof or versioned JSON.
Use `bubblepprof <command> -h` for the exact flags. `inspect` preserves
capture metadata alongside `analysis_error` if heap parsing or runtime recovery
fails, and returns a nonzero exit status.

## Understand the measurements

- **Reachable:** each object reached from the union of selected goroutine roots,
  counted once, with its recorded shallow size.
- **Exclusive:** selected reachable objects not reached from any other modeled
  goroutine or process-global root.
- **Shared:** selected reachable objects also reached from other modeled roots.
- **Pairwise shared:** intersection of two selections, which can include objects
  reached by additional goroutines or globals.

Exclusive and shared partition a selection's reachable set. Global, system, and
other overlaps can intersect each other. Results for different selectors are
not additive. These are reference relationships, not object ownership or
proportional estimates. They are not RSS, allocation rates, or dominator retained
sizes. Go's dump does not expose every runtime reference; inspect completeness
diagnostics and unresolved pointers before drawing conclusions.

See [investigation workflows](investigations.md) for four complete scenarios,
[capture controls](capture-reliability.md) for operational settings, and
[supported platforms](releases.md#supported-platforms) for compatibility.
