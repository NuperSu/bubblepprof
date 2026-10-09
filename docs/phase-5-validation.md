# Phase 5 validation

## Local Go coverage

Linux Go `go1.27.2-X:nodwarf5`:

```sh
go test ./...
GOARCH=386 go test ./...
```

Both full suites passed.

Coverage includes standard heap/goroutine profile capture and round trips,
separate timestamps, optional/associated profile members, duplicate and bounded
archive reads, genuine runtime allocation evidence, interior/ambiguous/missing
sample associations, returned evidence isolation, cycles and sharing,
native/JSON/pprof metric parity, signed pprof overflow rejection, and
`go tool pprof -top` for all three byte sample types.

## Real backend validation

Standalone Grafana Pyroscope **v2.4.0** (revision `1c9109674`) was downloaded from
its official GitHub release and run against local filesystem storage. Docker
was unavailable due to host socket permissions; the standalone binary test
completed instead. No server or binary is required by the offline analyzer.

The test server used this configuration (paths and ports can be changed):

```yaml
target: all
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 14040
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 14041
storage:
  backend: filesystem
  filesystem:
    dir: /tmp/bubble-pyroscope/data
tracing:
  enabled: false
```

Additional command flags:

```text
-architecture.storage=v1
-write-path=ingester
-usage-stats.enabled=false
-query-frontend.instance-addr=127.0.0.1
-query-scheduler.ring.instance-addr=127.0.0.1
-ingester.lifecycler.addr=127.0.0.1
-distributor.ring.instance-addr=127.0.0.1
-store-gateway.sharding-ring.instance-addr=127.0.0.1
-compactor.ring.instance-addr=127.0.0.1
-ingester.min-ready-duration=0
```

A real labeled goroutine held a 64 KiB payload. Its captured bundle was exported
through the CLI. `scripts/check_pyroscope.py` ingested the exported profile,
discovered custom types, filtered by service and the original `phase5=export`
selector label, and queried the backend flamegraphs. Verified totals:

| Metric | Native and backend total |
|---|---:|
| reachable_space | 66,296 bytes |
| exclusive_space | 65,536 bytes |
| shared_space | 760 bytes |
| reachable_objects | 6 |
| exclusive_objects | 1 |
| shared_objects | 5 |

These are recorded observations, not timing or machine-specific test thresholds.
The smoke check reads the generated native JSON for expectations on each run.
Every returned frame was `total` or an explicitly synthetic
`[bubblepprof summary]` frame. The test checks rendering data semantics through
the backend query API; it does not require a browser or Grafana UI.

Gotip was skipped as requested. Parca was not separately tested. Backend
interoperability checks are opt-in and do not require network services in the
routine portable Go suite.
