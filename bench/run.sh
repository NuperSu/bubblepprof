#!/usr/bin/env bash
# bench/run.sh — orchestrator for bubblepprof thesis benchmarks.
#
# Sweeps configurations of cmd/bench under /usr/bin/time -v (GNU time) and
# aggregates per-config JSONs into bench/results/summary.csv.
#
# Modes:
#   --quick       small static sweep (4 configs × 5 iterations).
#   --full        full static sweep (120 configs × 20 iterations).
#   --quick-live  small rotating-workload sweep.
#   --full-live   full rotating-workload sweep.
#
# Requirements: Linux + /usr/bin/time (GNU time) + python3.

set -euo pipefail

mode="${1:-}"
if [[ "$mode" != "--quick" && "$mode" != "--full" && "$mode" != "--quick-live" && "$mode" != "--full-live" ]]; then
    echo "usage: $0 --quick | --full | --quick-live | --full-live" >&2
    exit 2
fi

if [[ "$(uname)" != "Linux" ]]; then
    echo "run.sh: Linux only (depends on /usr/bin/time -v and /proc/self/status)" >&2
    exit 2
fi

if ! command -v /usr/bin/time >/dev/null 2>&1; then
    echo "run.sh: /usr/bin/time (GNU time) not found; install with: sudo pacman -S time   (or distro equivalent)" >&2
    exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
    echo "run.sh: python3 not found" >&2
    exit 2
fi

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
results_dir="$repo_root/bench/results"
mkdir -p "$results_dir"

bin="$results_dir/bench"
echo "build: $bin"
( cd "$repo_root" && go build -o "$bin" ./cmd/bench )

workload="static"
pre_measure_gc="true"
if [[ "$mode" == "--quick-live" || "$mode" == "--full-live" ]]; then
    workload="rotating"
    pre_measure_gc="false"
fi

# Configuration sweep.
if [[ "$mode" == "--quick" || "$mode" == "--quick-live" ]]; then
    heap_mbs=(50 200)
    goroutines_list=(100 1000)
    match_fractions=(1.0)
    gc_pres=(false)
    if [[ "$mode" == "--quick-live" ]]; then
        gc_pres=(false true)
    fi
    iterations=5
    warmup=2
else
    heap_mbs=(50 200 500 1000 2000)
    goroutines_list=(100 1000 5000 10000)
    match_fractions=(0.01 0.5 1.0)
    gc_pres=(false true)
    iterations=20
    warmup=3
fi

trace_one="${BENCH_TRACE:-1}"  # set BENCH_TRACE=0 to skip trace iteration

run_one() {
    local heap_mb="$1"
    local goroutines="$2"
    local match="$3"
    local gcpre="$4"
    local tag="heap=${heap_mb}_g=${goroutines}_match=${match}_gc=${gcpre}"
    if [[ "$workload" != "static" ]]; then
        tag="${workload}_${tag}"
    fi
    local json="$results_dir/${tag}.json"
    local timef="$results_dir/${tag}.time.txt"
    local trace=""
    if [[ "$trace_one" == "1" ]]; then
        trace="$results_dir/${tag}.trace"
    fi

    echo "==> $tag"
    local args=(
        -heap-mb "$heap_mb"
        -goroutines "$goroutines"
        -match-fraction "$match"
        -iterations "$iterations"
        -warmup "$warmup"
        -workload "$workload"
        -pre-measure-gc="$pre_measure_gc"
        -reset-vmhwm=true
        -tag "$tag"
        -out "$json"
    )
    if [[ "$gcpre" == "true" ]]; then
        args+=(-gc-pre)
    fi
    if [[ -n "$trace" ]]; then
        args+=(-trace "$trace")
    fi
    /usr/bin/time -v -o "$timef" "$bin" "${args[@]}"
}

for heap_mb in "${heap_mbs[@]}"; do
    for goroutines in "${goroutines_list[@]}"; do
        for match in "${match_fractions[@]}"; do
            for gcpre in "${gc_pres[@]}"; do
                run_one "$heap_mb" "$goroutines" "$match" "$gcpre"
            done
        done
    done
done

echo "==> aggregating into summary.csv"
python3 "$repo_root/bench/aggregate.py" "$results_dir" > "$results_dir/summary.csv"
echo "done. results in $results_dir"
