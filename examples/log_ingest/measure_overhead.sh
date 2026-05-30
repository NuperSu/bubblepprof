#!/usr/bin/env bash
# measure_overhead.sh — measure the per-call RSS overhead of /debug/memusage
# against a WARM, continuously-allocating server (examples/log_ingest).
#
# Why this exists
# ---------------
# The cmd/bench harness measures Compute against a *static* heap that is
# force-GC'd immediately before each call. That is the cold-target worst case
# and reports ~1x-heap peak-RSS overhead. A real service allocates
# continuously and keeps resident GOGC headroom, so the same transient parse
# allocation can be absorbed without new resident memory. This script measures
# that realistic regime, 100x, with an exact peak counter.
#
# Accuracy
# --------
#  * Peak RSS per call is read from the kernel's VmHWM high-water mark, which
#    the kernel updates at page-fault time -> it cannot miss a sub-second
#    spike the way a /proc sampler can.
#  * VmHWM is reset to current RSS before each call via
#    `echo 5 > /proc/PID/clear_refs` (Linux >= 4.0), so VmHWM-after is the true
#    peak *during that one call*.
#  * Control windows (no endpoint call, same duration) measure the server's own
#    background RSS drift, so the endpoint's marginal cost is isolated, not
#    confounded with the workload allocating anyway.
#
# Output: a CSV of every iteration plus an aggregate summary (mean/median/p95/
# max peak overhead, control drift, per-query breakdown, % of live heap).
#
# Requires: Linux, go, curl, python3. (jq optional; not used.)
set -uo pipefail

ADDR="${ADDR:-127.0.0.1:6060}"
N="${N:-100}"                  # measured endpoint calls
CONTROL="${CONTROL:-20}"       # control (no-call) windows
WARMUP_SECS="${WARMUP_SECS:-12}"
CSV="${CSV:-/tmp/memusage_overhead.csv}"
LOG="${LOG:-/tmp/log_ingest.run.log}"
BIN="${BIN:-/tmp/log_ingest_overhead}"
# Example flags. Defaults -> ~736 MiB live (24 ingesters * 24 MiB + 160 MiB dict).
LI_FLAGS=("${LI_FLAGS[@]:-}")
[ -z "${LI_FLAGS[*]}" ] && LI_FLAGS=(-dict-mb 160 -ring 48 -chunk-kb 512 -tenants 4 -streams 3 -shards 2)

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"

if [ "$(uname)" != "Linux" ]; then
  echo "measure_overhead.sh: Linux only (needs /proc and clear_refs)" >&2
  exit 2
fi
for t in go curl python3; do
  command -v "$t" >/dev/null 2>&1 || { echo "missing dependency: $t" >&2; exit 2; }
done

PID=""
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; }
trap cleanup EXIT INT TERM

# read VmRSS and VmHWM (kB) in one shot
rss_hwm() { awk '/^VmRSS:/{r=$2}/^VmHWM:/{h=$2}END{print r" "h}' "/proc/$1/status"; }
# reset peak RSS (VmHWM := VmRSS); returns nonzero if unsupported
reset_peak() { echo 5 > "/proc/$1/clear_refs" 2>/dev/null; }
post() { # $1=json -> body on stdout, http code on fd? we capture both
  curl -s -o /tmp/_mu_body -w '%{http_code}' -XPOST "http://$ADDR/debug/memusage" \
    -H 'Content-Type: application/json' -d "$1" 2>/dev/null
}

echo "== build =="
( cd "$repo_root" && go build -o "$BIN" ./examples/log_ingest ) || { echo "build failed" >&2; exit 1; }
# (equivalent to `go run ./examples/log_ingest`, but a built binary gives a
#  clean PID for /proc measurement instead of go run's wrapper child.)

echo "== start server: $BIN -addr $ADDR ${LI_FLAGS[*]} =="
"$BIN" -addr "$ADDR" "${LI_FLAGS[@]}" >"$LOG" 2>&1 &
PID=$!
echo "pid=$PID  log=$LOG"
echo "== warmup ${WARMUP_SECS}s (let the chunk rings fill) =="
sleep "$WARMUP_SECS"
kill -0 "$PID" 2>/dev/null || { echo "server died during warmup; see $LOG" >&2; tail -5 "$LOG" >&2; exit 1; }

# Verify label recovery works on this platform before spending minutes.
code="$(post '{"labels":{"service":"log-ingester"}}')"
body="$(cat /tmp/_mu_body)"
if [ "$code" != "200" ]; then
  echo "endpoint returned HTTP $code: $body" >&2
  echo "(if code=422 unsupported_runtime/string_missing, label recovery is unavailable on this Go/platform)" >&2
  exit 1
fi

# Verify clear_refs peak-reset actually works on this kernel.
reset_peak "$PID" || { echo "clear_refs not writable; cannot reset peak RSS" >&2; exit 1; }
read -r r0 h0 < <(rss_hwm "$PID")
if [ "$((h0 - r0))" -gt 4096 ]; then
  echo "WARN: VmHWM ($h0) >> VmRSS ($r0) after reset; clear_refs peak-reset may be unsupported." >&2
  echo "      Results would understate overhead. Aborting to avoid a misleading number." >&2
  exit 1
fi

QUERIES=(
  '{"labels":{"tenant":"atlas-bikes"}}'
  '{"labels":{"tenant":"atlas-bikes","stream":"app"}}'
  '{"labels":{"tenant":"atlas-bikes","stream":"app","shard":"0"}}'
  '{"labels":{"region":"eu-west"}}'
  '{"labels":{"tier":"enterprise"}}'
  '{"labels":{"service":"log-ingester"}}'
  '{"labels":{"tenant":"nonexistent"}}'
)

# Estimate a representative call duration (for matched control-window length).
echo "== calibrate call duration =="
durs=()
for i in 1 2 3 4 5; do
  t0=$(date +%s.%N); post "${QUERIES[5]}" >/dev/null; t1=$(date +%s.%N)
  durs+=("$(python3 -c "print($t1-$t0)")")
done
CTRL_SLEEP="$(python3 -c "import statistics,sys; print(round(statistics.median([float(x) for x in sys.argv[1:]]),3))" "${durs[@]}")"
echo "median call wall ~= ${CTRL_SLEEP}s (control windows use this)"

# NOTE: queries contain commas/quotes, so we store a query INDEX (qidx), never
# the raw query text, in this comma-separated CSV. The legend is printed below.
echo "iter,phase,qidx,http,matched,reachable_bytes,wall_s,rss_before_kb,hwm_after_kb,peak_overhead_kb,rss_after_kb,settle_kb" > "$CSV"

# ---- control phase: background drift with NO endpoint call ----
echo "== control: $CONTROL windows of ${CTRL_SLEEP}s, no endpoint call =="
for ((i=1; i<=CONTROL; i++)); do
  reset_peak "$PID"
  read -r rb hb < <(rss_hwm "$PID")
  sleep "$CTRL_SLEEP"
  read -r ra ha < <(rss_hwm "$PID")
  echo "$i,control,-1,-,-,-,$CTRL_SLEEP,$rb,$ha,$((ha-hb)),$ra,$((ra-rb))" >> "$CSV"
done

# ---- measured phase: N endpoint calls, exact peak per call ----
echo "== measured: $N endpoint calls (cycling ${#QUERIES[@]} queries) =="
for ((i=1; i<=N; i++)); do
  qidx=$(( (i-1) % ${#QUERIES[@]} ))
  q="${QUERIES[$qidx]}"
  reset_peak "$PID"
  read -r rb hb < <(rss_hwm "$PID")
  t0=$(date +%s.%N)
  code="$(post "$q")"; body="$(cat /tmp/_mu_body)"
  t1=$(date +%s.%N)
  read -r ra ha < <(rss_hwm "$PID")
  wall="$(python3 -c "print(round($t1-$t0,4))")"
  matched="$(python3 -c "import json,sys;print(json.loads(sys.argv[1]).get('matched_goroutines',-1))" "$body" 2>/dev/null || echo -1)"
  reach="$(python3 -c "import json,sys;print(json.loads(sys.argv[1]).get('reachable_bytes',-1))" "$body" 2>/dev/null || echo -1)"
  echo "$i,call,$qidx,$code,$matched,$reach,$wall,$rb,$ha,$((ha-hb)),$ra,$((ra-rb))" >> "$CSV"
  if (( i % 10 == 0 )); then printf '  %d/%d (last peak +%d kB, matched=%s)\n' "$i" "$N" "$((ha-hb))" "$matched"; fi
done

heap_line="$(grep -oE 'heap_alloc=[0-9]+MiB' "$LOG" | tail -1)"
heap_mib="$(echo "${heap_line:-heap_alloc=0MiB}" | grep -oE '[0-9]+')"

echo
echo "================= SUMMARY ================="
echo "live heap (reporter heap_alloc, latest): ${heap_mib} MiB"
echo "query legend:"
for idx in "${!QUERIES[@]}"; do echo "  [$idx] ${QUERIES[$idx]}"; done
python3 - "$CSV" "$heap_mib" <<'PY'
import csv, sys, statistics as st
csv_path, heap_mib = sys.argv[1], float(sys.argv[2] or 0)
rows=list(csv.DictReader(open(csv_path)))
def col(rows,k): return [float(r[k]) for r in rows]
def pct(v,p):
    if not v: return 0.0
    s=sorted(v); k=(len(s)-1)*p; lo=int(k); hi=min(lo+1,len(s)-1)
    return s[lo]+(s[hi]-s[lo])*(k-lo)
def mib(kb): return kb/1024.0
def line(name, v):
    print(f"{name:<26} n={len(v):<4} mean={mib(st.mean(v)):7.1f}  median={mib(st.median(v)):7.1f}"
          f"  p95={mib(pct(v,.95)):7.1f}  max={mib(max(v)):7.1f}   (MiB)")

calls=[r for r in rows if r['phase']=='call']
ctrls=[r for r in rows if r['phase']=='control']
peak=col(calls,'peak_overhead_kb')
drift=col(ctrls,'peak_overhead_kb') if ctrls else [0.0]
settle=col(calls,'settle_kb')

print("\n-- per-call PEAK RSS overhead (kernel VmHWM, exact) --")
line("raw peak overhead", peak)
print("\n-- control: background drift, NO endpoint call, same window --")
line("background drift", drift)
adj=[p-st.median(drift) for p in peak]
print("\n-- drift-adjusted (peak - median background drift) --")
line("endpoint-attributable", adj)
print("\n-- steady-state: RSS settle after call returns --")
line("settle (rss_after-before)", settle)

med_peak_mib=mib(st.median(peak))
if heap_mib>0:
    print(f"\nmedian peak overhead = {med_peak_mib:.1f} MiB = {100*med_peak_mib/heap_mib:.2f}% of {heap_mib:.0f} MiB live heap")
    print(f"median drift-adjusted = {mib(st.median(adj)):.1f} MiB = {100*mib(st.median(adj))/heap_mib:.2f}% of live heap")

print("\n-- per-query (drift-adjusted peak overhead, MiB) --")
from collections import defaultdict
byq=defaultdict(list); matched_by={}
md=st.median(drift)
for r in calls:
    byq[r['qidx']].append(float(r['peak_overhead_kb'])-md)
    matched_by[r['qidx']]=r['matched']
for qi in sorted(byq, key=int):
    v=byq[qi]
    print(f"  qidx={qi}  matched={matched_by[qi]:<4} n={len(v):<3} "
          f"median={mib(st.median(v)):6.1f}  p95={mib(pct(v,.95)):6.1f}  max={mib(max(v)):6.1f}")
PY
echo "==========================================="
echo "raw CSV: $CSV"
