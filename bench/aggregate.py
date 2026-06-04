#!/usr/bin/env python3
"""Aggregate cmd/bench per-config JSON outputs into a single CSV.

Walks <results_dir>/*.json (one per configuration), extracts the summary
statistics and a few config fields, and parses the matching <tag>.time.txt
(GNU `/usr/bin/time -v`) for OS-level cross-check fields. Emits a CSV to
stdout with one row per configuration.

Usage: aggregate.py <results_dir>
"""

from __future__ import annotations

import csv
import json
import re
import sys
from pathlib import Path

METRICS = [
    "wall_ns",
    "max_heartbeat_pause_ns",
    "go_heap_alloc_delta_b",
    "go_total_alloc_delta_b",
    "vm_rss_after_kb",
    "vm_hwm_before_kb",
    "vm_hwm_after_kb",
    "vm_peak_delta_kb",
    "reachable_bytes",
]
STATS = ["mean", "stddev", "min", "max", "p50", "p95", "p99"]


def parse_time_file(path: Path) -> dict[str, str]:
    """Parse the subset of `/usr/bin/time -v` lines we care about."""
    out: dict[str, str] = {}
    if not path.exists():
        return out
    text = path.read_text(errors="replace")
    patterns = {
        "time_maxrss_kb": r"Maximum resident set size \(kbytes\):\s*(\d+)",
        "time_user_s": r"User time \(seconds\):\s*([\d.]+)",
        "time_sys_s": r"System time \(seconds\):\s*([\d.]+)",
        "time_elapsed": r"Elapsed \(wall clock\) time.*?:\s*([\d:.]+)",
        "time_minor_faults": r"Minor \(reclaiming a frame\) page faults:\s*(\d+)",
        "time_major_faults": r"Major \(requiring I/O\) page faults:\s*(\d+)",
        "time_voluntary_ctx": r"Voluntary context switches:\s*(\d+)",
        "time_involuntary_ctx": r"Involuntary context switches:\s*(\d+)",
    }
    for key, pat in patterns.items():
        m = re.search(pat, text)
        if m:
            out[key] = m.group(1)
    return out


def row_for_json(path: Path) -> dict[str, str]:
    data = json.loads(path.read_text())
    cfg = data.get("config", {})
    summary = data.get("summary", {})

    row: dict[str, str] = {
        "tag": cfg.get("tag", path.stem),
        "heap_mb": str(cfg.get("heap_mb", "")),
        "goroutines": str(cfg.get("goroutines", "")),
        "match_fraction": str(cfg.get("match_fraction", "")),
        "gc_pre": str(cfg.get("gc_pre", "")).lower(),
        "pre_measure_gc": str(cfg.get("pre_measure_gc", "")).lower(),
        "reset_vmhwm": str(cfg.get("reset_vmhwm", "")).lower(),
        "workload": str(cfg.get("workload", "")),
        "ring": str(cfg.get("ring", "")),
        "rotate_interval_ms": str(cfg.get("rotate_interval_ms", "")),
        "iterations": str(cfg.get("iterations", "")),
        "warmup": str(cfg.get("warmup", "")),
        "go_version": str(data.get("go", {}).get("version", "")),
        "goarch": str(data.get("go", {}).get("goarch", "")),
    }
    for metric in METRICS:
        s = summary.get(metric, {})
        for stat in STATS:
            row[f"{metric}_{stat}"] = str(s.get(stat, ""))

    time_path = path.with_suffix(".time.txt")
    row.update(parse_time_file(time_path))
    return row


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    results_dir = Path(argv[1])
    if not results_dir.is_dir():
        print(f"not a directory: {results_dir}", file=sys.stderr)
        return 2

    rows = []
    for json_path in sorted(results_dir.glob("*.json")):
        if json_path.name == "summary.csv":
            continue
        try:
            rows.append(row_for_json(json_path))
        except (json.JSONDecodeError, KeyError) as e:
            print(f"warning: skipping {json_path}: {e}", file=sys.stderr)

    if not rows:
        print("no JSON results found", file=sys.stderr)
        return 1

    # Union of all keys across rows, with a stable preferred ordering for
    # the columns the thesis most often cites.
    preferred = [
        "tag", "heap_mb", "goroutines", "match_fraction", "gc_pre",
        "pre_measure_gc", "reset_vmhwm", "workload", "ring", "rotate_interval_ms",
        "iterations", "warmup", "go_version", "goarch",
        "wall_ns_mean", "wall_ns_stddev", "wall_ns_p50", "wall_ns_p95", "wall_ns_p99",
        "max_heartbeat_pause_ns_mean", "max_heartbeat_pause_ns_p50",
        "max_heartbeat_pause_ns_p95", "max_heartbeat_pause_ns_p99",
        "go_heap_alloc_delta_b_mean", "go_total_alloc_delta_b_mean",
        "vm_rss_after_kb_mean", "vm_hwm_after_kb_mean",
        "vm_peak_delta_kb_mean",
        "reachable_bytes_mean",
        "time_maxrss_kb", "time_user_s", "time_sys_s", "time_elapsed",
        "time_minor_faults", "time_major_faults",
        "time_voluntary_ctx", "time_involuntary_ctx",
    ]
    rest = sorted({k for r in rows for k in r.keys()} - set(preferred))
    fieldnames = preferred + rest

    w = csv.DictWriter(sys.stdout, fieldnames=fieldnames, extrasaction="ignore")
    w.writeheader()
    for r in rows:
        w.writerow(r)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
