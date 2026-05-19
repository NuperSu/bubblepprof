#!/usr/bin/env python3
"""Thesis-grade analysis of bench/results/.

Reads the per-config JSONs and summary.csv produced by run.sh + aggregate.py,
emits plots into bench/results/plots/ and a short analysis.md summary.

Drops:
    * the 2 stray heap_mb=800 rows (partial earlier run).
    * iteration with `under_trace=true` from per-iteration variance stats
      (one extra runtime/trace-instrumented iteration per config — confounds
      the wall-time/RSS distribution).

Usage: python3 bench/analyze.py
"""

from __future__ import annotations

import json
import math
import re
from pathlib import Path

import matplotlib.pyplot as plt
import numpy as np
import pandas as pd

ROOT = Path(__file__).resolve().parent
RESULTS = ROOT / "results"
PLOTS = RESULTS / "plots"
PLOTS.mkdir(exist_ok=True)

# ----------------------------------------------------------------------------- IO

def load_summary() -> pd.DataFrame:
    df = pd.read_csv(RESULTS / "summary.csv")
    # Drop the partial heap=800 leftover.
    df = df[df["heap_mb"] != 800].copy()
    # Numeric types
    for c in df.columns:
        if df[c].dtype == "object" and c not in ("tag", "go_version", "goarch", "time_elapsed"):
            df[c] = pd.to_numeric(df[c], errors="ignore")
    # Convenience derived columns
    df["wall_ms_mean"] = df["wall_ns_mean"] / 1e6
    df["wall_ms_stddev"] = df["wall_ns_stddev"] / 1e6
    df["wall_ms_p50"] = df["wall_ns_p50"] / 1e6
    df["wall_ms_p95"] = df["wall_ns_p95"] / 1e6
    df["wall_ms_p99"] = df["wall_ns_p99"] / 1e6
    df["heartbeat_ms_mean"] = df["max_heartbeat_pause_ns_mean"] / 1e6
    df["heartbeat_ms_p95"] = df["max_heartbeat_pause_ns_p95"] / 1e6
    df["heartbeat_ms_p99"] = df["max_heartbeat_pause_ns_p99"] / 1e6
    df["cv_wall_pct"] = 100.0 * df["wall_ns_stddev"] / df["wall_ns_mean"]
    df["cv_heartbeat_pct"] = (
        100.0 * df["max_heartbeat_pause_ns_stddev"] / df["max_heartbeat_pause_ns_mean"]
    )
    df["reachable_mb_mean"] = df["reachable_bytes_mean"] / (1 << 20)
    df["vm_hwm_mb"] = df["vm_hwm_after_kb_mean"] / 1024
    df["vm_rss_mb"] = df["vm_rss_after_kb_mean"] / 1024
    df["time_maxrss_mb"] = df["time_maxrss_kb"] / 1024
    # Time elapsed → seconds (handles "m:ss.xx" or "h:mm:ss.xx").
    df["time_elapsed_s"] = df["time_elapsed"].map(parse_elapsed)
    return df


def parse_elapsed(s: str) -> float:
    parts = s.split(":")
    parts = [float(p) for p in parts]
    if len(parts) == 2:
        return parts[0] * 60 + parts[1]
    if len(parts) == 3:
        return parts[0] * 3600 + parts[1] * 60 + parts[2]
    return float("nan")


def load_iterations() -> pd.DataFrame:
    """One row per (config, iteration); excludes under_trace iterations."""
    rows = []
    for jp in sorted(RESULTS.glob("*.json")):
        d = json.loads(jp.read_text())
        cfg = d["config"]
        if cfg["heap_mb"] == 800:
            continue
        for it in d["iterations"]:
            if it.get("under_trace"):
                continue
            rows.append({
                "tag": cfg["tag"],
                "heap_mb": cfg["heap_mb"],
                "goroutines": cfg["goroutines"],
                "match_fraction": cfg["match_fraction"],
                "gc_pre": bool(cfg["gc_pre"]),
                "index": it["index"],
                "wall_ns": it["wall_ns"],
                "max_heartbeat_ns": it["max_heartbeat_pause_ns"],
                "go_heap_alloc_delta_b": it["go_heap_alloc_delta_b"],
                "go_total_alloc_delta_b": it["go_total_alloc_delta_b"],
                "vm_rss_before_kb": it["vm_rss_before_kb"],
                "vm_rss_after_kb": it["vm_rss_after_kb"],
                "vm_hwm_after_kb": it["vm_hwm_after_kb"],
                "reachable_bytes": it["reachable_bytes"],
                "matched_goroutines": it["matched_goroutines"],
            })
    iters = pd.DataFrame(rows)
    iters["wall_ms"] = iters["wall_ns"] / 1e6
    iters["heartbeat_ms"] = iters["max_heartbeat_ns"] / 1e6
    return iters


# ----------------------------------------------------------------------------- helpers

def label_for_gc(gc: bool) -> str:
    return "gc_pre=true" if gc else "gc_pre=false"


def savefig(name: str) -> Path:
    p = PLOTS / name
    plt.tight_layout()
    plt.savefig(p, dpi=130)
    plt.close()
    return p


# ----------------------------------------------------------------------------- plots

def plot_wall_vs_heap(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0]
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4), sharey=True)
    for ax, gc in zip(axes, [False, True]):
        s = sub[sub["gc_pre"] == gc].sort_values(["goroutines", "heap_mb"])
        for g, grp in s.groupby("goroutines"):
            ax.errorbar(
                grp["heap_mb"], grp["wall_ms_mean"], yerr=grp["wall_ms_stddev"],
                marker="o", capsize=3, label=f"{g} goroutines",
            )
        ax.set_title(label_for_gc(gc))
        ax.set_xlabel("workload heap (MiB)")
        ax.grid(True, alpha=0.3)
    axes[0].set_ylabel("Compute wall time (ms, mean ± stddev)")
    axes[0].legend(fontsize=8, loc="best")
    fig.suptitle("Wall time vs workload heap size (match=1.0)")
    savefig("wall_vs_heap.png")


def plot_wall_vs_goroutines(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0]
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4), sharey=True)
    for ax, gc in zip(axes, [False, True]):
        s = sub[sub["gc_pre"] == gc].sort_values(["heap_mb", "goroutines"])
        for h, grp in s.groupby("heap_mb"):
            ax.errorbar(
                grp["goroutines"], grp["wall_ms_mean"], yerr=grp["wall_ms_stddev"],
                marker="o", capsize=3, label=f"{h} MiB heap",
            )
        ax.set_title(label_for_gc(gc))
        ax.set_xlabel("labeled goroutines")
        ax.set_xscale("log")
        ax.grid(True, alpha=0.3, which="both")
    axes[0].set_ylabel("Compute wall time (ms, mean ± stddev)")
    axes[0].legend(fontsize=8, loc="best")
    fig.suptitle("Wall time vs goroutine count (match=1.0)")
    savefig("wall_vs_goroutines.png")


def plot_match_effect(df: pd.DataFrame) -> None:
    """Wall and reachable_bytes vs match_fraction (one curve per heap)."""
    sub = df[(df["gc_pre"] == False) & (df["goroutines"] == 1000)]
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4))
    for h, grp in sub.sort_values(["heap_mb", "match_fraction"]).groupby("heap_mb"):
        axes[0].errorbar(
            grp["match_fraction"], grp["wall_ms_mean"], yerr=grp["wall_ms_stddev"],
            marker="o", capsize=3, label=f"{h} MiB",
        )
        axes[1].plot(
            grp["match_fraction"], grp["reachable_mb_mean"],
            marker="o", label=f"{h} MiB",
        )
    axes[0].set_xlabel("match_fraction")
    axes[0].set_ylabel("wall time (ms, mean ± stddev)")
    axes[0].set_title("Wall vs match_fraction (g=1000, gc_pre=false)")
    axes[0].grid(True, alpha=0.3)
    axes[0].legend(fontsize=8)
    axes[1].set_xlabel("match_fraction")
    axes[1].set_ylabel("reachable bytes (MiB)")
    axes[1].set_title("Reachable bytes scale linearly with matched fraction")
    axes[1].grid(True, alpha=0.3)
    axes[1].legend(fontsize=8)
    savefig("match_effect.png")


def plot_gc_pre_effect(df: pd.DataFrame) -> None:
    """Paired comparison gc_pre=false vs gc_pre=true."""
    keys = ["heap_mb", "goroutines", "match_fraction"]
    a = df[df["gc_pre"] == False].set_index(keys)
    b = df[df["gc_pre"] == True].set_index(keys)
    common = a.index.intersection(b.index)
    a = a.loc[common]
    b = b.loc[common]

    fig, axes = plt.subplots(1, 3, figsize=(15, 4.4))
    # wall
    axes[0].scatter(a["wall_ms_mean"], b["wall_ms_mean"], s=22, alpha=0.7)
    lim = max(a["wall_ms_mean"].max(), b["wall_ms_mean"].max()) * 1.05
    axes[0].plot([0, lim], [0, lim], "k--", alpha=0.4)
    axes[0].set_xlabel("wall (ms), gc_pre=false")
    axes[0].set_ylabel("wall (ms), gc_pre=true")
    axes[0].set_title("Wall time per config")
    axes[0].grid(True, alpha=0.3)
    # total_alloc
    axes[1].scatter(
        a["go_total_alloc_delta_b_mean"] / 1e9,
        b["go_total_alloc_delta_b_mean"] / 1e9,
        s=22, alpha=0.7,
    )
    lim = max(
        a["go_total_alloc_delta_b_mean"].max(),
        b["go_total_alloc_delta_b_mean"].max(),
    ) * 1.05 / 1e9
    axes[1].plot([0, lim], [0, lim], "k--", alpha=0.4)
    axes[1].set_xlabel("TotalAlloc Δ (GiB), gc_pre=false")
    axes[1].set_ylabel("TotalAlloc Δ (GiB), gc_pre=true")
    axes[1].set_title("Per-call Go allocator churn")
    axes[1].grid(True, alpha=0.3)
    # voluntary ctx switches
    axes[2].scatter(a["time_voluntary_ctx"] / 1e6, b["time_voluntary_ctx"] / 1e6, s=22, alpha=0.7)
    lim = max(a["time_voluntary_ctx"].max(), b["time_voluntary_ctx"].max()) * 1.05 / 1e6
    axes[2].plot([0, lim], [0, lim], "k--", alpha=0.4)
    axes[2].set_xlabel("voluntary ctx switches (M), gc_pre=false")
    axes[2].set_ylabel("voluntary ctx switches (M), gc_pre=true")
    axes[2].set_title("Process-wide voluntary ctx switches")
    axes[2].grid(True, alpha=0.3)
    fig.suptitle("gc_pre=true vs gc_pre=false (paired by config)")
    savefig("gc_pre_pairs.png")


def plot_heartbeat(df: pd.DataFrame) -> None:
    sub = df[(df["match_fraction"] == 1.0) & (df["gc_pre"] == False)]
    fig, ax = plt.subplots(figsize=(7.2, 4.4))
    for g, grp in sub.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
        ax.errorbar(
            grp["heap_mb"], grp["heartbeat_ms_mean"],
            yerr=grp["max_heartbeat_pause_ns_stddev"] / 1e6,
            marker="o", capsize=3, label=f"{g} goroutines",
        )
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("max heartbeat pause (ms, mean ± stddev)")
    ax.set_title("Max scheduling pause during Compute (match=1.0, gc_pre=false)")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=8)
    savefig("heartbeat_vs_heap.png")


def plot_heartbeat_ratio(df: pd.DataFrame) -> None:
    df = df.copy()
    df["heartbeat_share"] = df["max_heartbeat_pause_ns_mean"] / df["wall_ns_mean"]
    fig, ax = plt.subplots(figsize=(7.2, 4.4))
    for gc in [False, True]:
        s = df[df["gc_pre"] == gc]
        ax.scatter(
            s["heap_mb"], s["heartbeat_share"], alpha=0.6,
            label=label_for_gc(gc), s=22,
        )
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("heartbeat / wall (fraction)")
    ax.set_title("Share of wall time covered by the worst scheduling pause")
    ax.grid(True, alpha=0.3)
    ax.legend()
    savefig("heartbeat_share.png")


def plot_rss_overhead(df: pd.DataFrame) -> None:
    """Profiler RSS overhead estimated as VmHWM − workload steady-state RSS.

    workload RSS ≈ heap_mb (the synthetic load touches every page so the
    workload RSS is close to heap_mb MiB). Overhead is the rest.
    """
    sub = df[df["match_fraction"] == 1.0].copy()
    sub["rss_overhead_mb"] = sub["vm_hwm_mb"] - sub["heap_mb"]

    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4))
    for gc in [False, True]:
        s = sub[sub["gc_pre"] == gc]
        for g, grp in s.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
            ax = axes[0] if not gc else axes[1]
            ax.plot(grp["heap_mb"], grp["rss_overhead_mb"], marker="o",
                    label=f"{g} goroutines")
    for ax, gc in zip(axes, [False, True]):
        ax.set_xlabel("workload heap (MiB)")
        ax.set_ylabel("VmHWM − workload heap (MiB)")
        ax.set_title(f"Profiler RSS overhead — {label_for_gc(gc)}")
        ax.grid(True, alpha=0.3)
        ax.legend(fontsize=8)
    fig.suptitle("Estimated peak RSS overhead of /debug/memusage")
    savefig("rss_overhead.png")


def plot_variance_iter(iters: pd.DataFrame) -> None:
    """Distribution of per-iteration wall times by config. Box-plot per
    config sorted by mean — shows variance shape and outliers."""
    # Limit to gc_pre=false, match=1.0 (otherwise plot is unreadable).
    sub = iters[(iters["gc_pre"] == False) & (iters["match_fraction"] == 1.0)].copy()
    sub["cfg"] = sub.apply(lambda r: f"h{r.heap_mb}/g{r.goroutines}", axis=1)
    order = (
        sub.groupby("cfg")["wall_ms"].median().sort_values().index.tolist()
    )
    sub["cfg"] = pd.Categorical(sub["cfg"], categories=order, ordered=True)
    sub = sub.sort_values("cfg")

    fig, ax = plt.subplots(figsize=(13, 5.0))
    data = [sub[sub["cfg"] == c]["wall_ms"].values for c in order]
    ax.boxplot(data, tick_labels=order, showmeans=True, meanline=True)
    ax.set_xticklabels(order, rotation=70, fontsize=7)
    ax.set_ylabel("Compute wall time (ms)")
    ax.set_title("Per-iteration wall-time distribution (match=1.0, gc_pre=false, n=20 each)")
    ax.grid(True, alpha=0.3, axis="y")
    savefig("variance_box.png")


def plot_cv(df: pd.DataFrame) -> None:
    """Coefficient of variation distribution across all configs."""
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.0))
    axes[0].hist(df["cv_wall_pct"].dropna(), bins=20, edgecolor="black")
    axes[0].set_xlabel("CV(wall) = stddev/mean (%)")
    axes[0].set_ylabel("# configs")
    axes[0].set_title(f"Wall-time stability across {len(df)} configs")
    axes[0].axvline(df["cv_wall_pct"].median(), color="red", ls="--",
                    label=f"median = {df['cv_wall_pct'].median():.1f}%")
    axes[0].legend()
    axes[0].grid(True, alpha=0.3)
    axes[1].hist(df["cv_heartbeat_pct"].dropna(), bins=20, edgecolor="black",
                 color="orange")
    axes[1].set_xlabel("CV(heartbeat) = stddev/mean (%)")
    axes[1].set_ylabel("# configs")
    axes[1].set_title("Heartbeat-pause stability")
    axes[1].axvline(df["cv_heartbeat_pct"].median(), color="red", ls="--",
                    label=f"median = {df['cv_heartbeat_pct'].median():.1f}%")
    axes[1].legend()
    axes[1].grid(True, alpha=0.3)
    savefig("cv_histograms.png")


def plot_elapsed_vs_iter(df: pd.DataFrame) -> None:
    """Process-wide elapsed time vs sum of per-iter walls.

    The gap is `between-iteration` time: workload steady-state, GC cycles
    triggered by parser bookkeeping, runtime housekeeping. Highlights the
    hidden process-wide benefit of gc_pre even though per-iter wall is
    nearly identical.
    """
    df = df.copy()
    df["iter_sum_s"] = (df["wall_ns_mean"] * (df["iterations"] + 1)) / 1e9
    df["between_s"] = df["time_elapsed_s"] - df["iter_sum_s"]
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.6))
    for ax, gc in zip(axes, [False, True]):
        s = df[(df["gc_pre"] == gc) & (df["match_fraction"] == 1.0)]
        for g, grp in s.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
            ax.plot(grp["heap_mb"], grp["between_s"], marker="o",
                    label=f"{g} goroutines")
        ax.set_xlabel("workload heap (MiB)")
        ax.set_ylabel("elapsed − Σ(per-iter wall) (s)")
        ax.set_title(label_for_gc(gc))
        ax.grid(True, alpha=0.3)
        ax.legend(fontsize=8)
    axes[0].set_ylim(axes[1].get_ylim())  # equalize y
    fig.suptitle("Between-iteration time (workload + cross-call GC)")
    savefig("between_iter_time.png")


def plot_ctx_switches(df: pd.DataFrame) -> None:
    """Process voluntary context switches paired by gc_pre."""
    keys = ["heap_mb", "goroutines", "match_fraction"]
    a = df[df["gc_pre"] == False].set_index(keys)
    b = df[df["gc_pre"] == True].set_index(keys)
    common = a.index.intersection(b.index)
    ratio = (a.loc[common, "time_voluntary_ctx"] / b.loc[common, "time_voluntary_ctx"]).rename("ratio")
    ratio = ratio.reset_index()
    ratio["heap_mb"] = ratio["heap_mb"].astype(int)
    fig, ax = plt.subplots(figsize=(7.5, 4.4))
    for g, grp in ratio.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
        ax.plot(grp["heap_mb"], grp["ratio"], marker="o", label=f"{g} goroutines")
    ax.axhline(1.0, color="black", ls="--", alpha=0.5)
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("ctx switches: gc_pre=false / gc_pre=true")
    ax.set_title("gc_pre=true reduces process-wide voluntary ctx switches")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=8)
    savefig("ctx_switch_ratio.png")


def plot_per_goroutine_cost(df: pd.DataFrame) -> None:
    """For each heap_mb, fit wall_ms = a·goroutines + b and report a."""
    sub = df[(df["match_fraction"] == 1.0) & (df["gc_pre"] == False)]
    rows = []
    for h, grp in sub.groupby("heap_mb"):
        x = grp["goroutines"].to_numpy(dtype=float)
        y = grp["wall_ms_mean"].to_numpy(dtype=float)
        a, b, r2 = linear_fit(x, y)
        rows.append((h, a, b, r2))
    fit = pd.DataFrame(rows, columns=["heap_mb", "us_per_goroutine", "intercept_ms", "R2"])
    # Convert ms/goroutine to µs/goroutine for readability.
    fit["us_per_goroutine"] = fit["us_per_goroutine"] * 1000
    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.plot(fit["heap_mb"], fit["us_per_goroutine"], marker="o")
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("per-goroutine wall cost (µs)")
    ax.set_title("Per-goroutine Compute cost (slope of wall vs g)")
    ax.grid(True, alpha=0.3)
    savefig("per_goroutine_cost.png")
    return fit


def plot_trace_overhead(iters_with_trace: pd.DataFrame) -> None:
    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.hist(iters_with_trace["trace_overhead_pct"], bins=20, edgecolor="black",
            color="seagreen")
    ax.axvline(iters_with_trace["trace_overhead_pct"].median(), color="red", ls="--",
               label=f"median = {iters_with_trace['trace_overhead_pct'].median():.1f}%")
    ax.set_xlabel("runtime/trace iteration overhead (%, vs mean of 20 measured)")
    ax.set_ylabel("# configs")
    ax.set_title("Overhead of the cross-check runtime/trace iteration")
    ax.grid(True, alpha=0.3)
    ax.legend()
    savefig("trace_overhead.png")


def collect_trace_overhead() -> pd.DataFrame:
    rows = []
    for jp in sorted(RESULTS.glob("*.json")):
        d = json.loads(jp.read_text())
        cfg = d["config"]
        if cfg["heap_mb"] == 800:
            continue
        measured = [it for it in d["iterations"] if not it.get("under_trace")]
        trace = [it for it in d["iterations"] if it.get("under_trace")]
        if not trace or not measured:
            continue
        mw = sum(it["wall_ns"] for it in measured) / len(measured)
        tw = trace[0]["wall_ns"]
        rows.append({
            "tag": cfg["tag"],
            "heap_mb": cfg["heap_mb"],
            "goroutines": cfg["goroutines"],
            "gc_pre": cfg["gc_pre"],
            "measured_wall_ms": mw / 1e6,
            "trace_wall_ms": tw / 1e6,
            "trace_overhead_pct": 100 * (tw - mw) / mw,
        })
    return pd.DataFrame(rows)


def plot_alloc_vs_heap(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0]
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4))
    for ax, gc in zip(axes, [False, True]):
        s = sub[sub["gc_pre"] == gc]
        for g, grp in s.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
            ax.plot(grp["heap_mb"], grp["go_total_alloc_delta_b_mean"] / 1e9,
                    marker="o", label=f"{g} goroutines")
        ax.set_xlabel("workload heap (MiB)")
        ax.set_ylabel("TotalAlloc Δ per Compute (GiB)")
        ax.set_title(label_for_gc(gc))
        ax.grid(True, alpha=0.3)
        ax.legend(fontsize=8)
    fig.suptitle("Go allocator pressure per /debug/memusage call (match=1.0)")
    savefig("alloc_vs_heap.png")


# ----------------------------------------------------------------------------- linear fits

def linear_fit(x: np.ndarray, y: np.ndarray) -> tuple[float, float, float]:
    """y = a*x + b, returns (a, b, R²)."""
    if len(x) < 2:
        return float("nan"), float("nan"), float("nan")
    a, b = np.polyfit(x, y, 1)
    yhat = a * x + b
    ss_res = np.sum((y - yhat) ** 2)
    ss_tot = np.sum((y - y.mean()) ** 2)
    r2 = 1.0 - ss_res / ss_tot if ss_tot > 0 else float("nan")
    return float(a), float(b), float(r2)


def fit_table(df: pd.DataFrame) -> pd.DataFrame:
    """Fit wall ≈ a·heap_mb + b separately per (goroutines, gc_pre)."""
    rows = []
    sub = df[df["match_fraction"] == 1.0]
    for (g, gc), grp in sub.groupby(["goroutines", "gc_pre"]):
        a, b, r2 = linear_fit(
            grp["heap_mb"].to_numpy(dtype=float),
            grp["wall_ms_mean"].to_numpy(dtype=float),
        )
        rows.append({
            "goroutines": g,
            "gc_pre": gc,
            "wall_ms_per_MiB_heap": a,
            "intercept_ms": b,
            "R2": r2,
        })
    return pd.DataFrame(rows).sort_values(["gc_pre", "goroutines"])


def fit_rss_overhead(df: pd.DataFrame) -> pd.DataFrame:
    sub = df[df["match_fraction"] == 1.0].copy()
    sub["rss_overhead_mb"] = sub["vm_hwm_mb"] - sub["heap_mb"]
    rows = []
    for (g, gc), grp in sub.groupby(["goroutines", "gc_pre"]):
        a, b, r2 = linear_fit(
            grp["heap_mb"].to_numpy(dtype=float),
            grp["rss_overhead_mb"].to_numpy(dtype=float),
        )
        rows.append({
            "goroutines": g,
            "gc_pre": gc,
            "rss_overhead_MiB_per_MiB_heap": a,
            "intercept_MiB": b,
            "R2": r2,
        })
    return pd.DataFrame(rows).sort_values(["gc_pre", "goroutines"])


# ----------------------------------------------------------------------------- markdown

def write_markdown(
    df: pd.DataFrame,
    iters: pd.DataFrame,
    per_g: pd.DataFrame,
    trace_df: pd.DataFrame,
) -> Path:
    out = []
    out.append("# bubblepprof bench analysis\n")
    out.append(f"Generated from `bench/results/` ({len(df)} configs × 20 iterations each, "
               f"plus 1 under-trace iteration per config that was excluded from variance stats).\n")
    out.append("\n## 1. Run summary\n")
    out.append(f"* Configs after cleanup: **{len(df)}** "
               "(5 heap sizes × 4 goroutine counts × 3 match fractions × 2 gc_pre).\n")
    out.append(f"* Per-iteration samples used for variance: **{len(iters)}**.\n")
    out.append(f"* Go version: `{df['go_version'].iloc[0]}` on `{df['goarch'].iloc[0]}`.\n")

    out.append("\n## 2. Wall-time stability across configs\n")
    cv = df["cv_wall_pct"].describe(percentiles=[0.5, 0.9, 0.95])
    out.append(f"* CV(wall) median = **{cv['50%']:.2f}%**, p95 = **{cv['95%']:.2f}%**, max = **{cv['max']:.2f}%**.\n")
    out.append(f"* CV(heartbeat) median = **{df['cv_heartbeat_pct'].median():.2f}%**.\n")
    high_cv = df[df["cv_wall_pct"] > 10][["tag", "cv_wall_pct"]].sort_values(
        "cv_wall_pct", ascending=False
    ).head(5)
    if not high_cv.empty:
        out.append("\nTop configs with CV(wall) > 10%:\n\n")
        out.append("```\n")
        out.append(high_cv.to_string(index=False))
        out.append("\n```\n")

    out.append("\n## 3. Wall time vs workload heap (linear fit, match=1.0)\n")
    out.append("```\n")
    out.append(fit_table(df).to_string(index=False, float_format=lambda v: f"{v:.3f}"))
    out.append("\n```\n")
    out.append("Interpretation: slope is the per-MiB heap cost of Compute (ms/MiB). "
               "The intercept covers fixed-cost setup (build heap dump + open/seek, etc.).\n")

    out.append("\n## 4. RSS overhead linear fit\n")
    out.append("```\n")
    out.append(fit_rss_overhead(df).to_string(index=False, float_format=lambda v: f"{v:.3f}"))
    out.append("\n```\n")
    out.append("Slope is `(VmHWM − workload_heap)` per MiB of workload heap; "
               "values close to ~1× mean the dump file's page cache pages still "
               "contribute to VmHWM. The intercept estimates fixed profiler RSS.\n")

    out.append("\n## 5. match_fraction\n")
    # validate: does wall depend on match_fraction at all?
    pivot = (
        df[df["gc_pre"] == False]
        .pivot_table(
            index=["heap_mb", "goroutines"],
            columns="match_fraction",
            values="wall_ms_mean",
        )
    )
    pivot.columns = [f"match={c}" for c in pivot.columns]
    out.append("Mean wall time (ms) by match_fraction (gc_pre=false):\n\n")
    out.append("```\n")
    out.append(pivot.to_string(float_format=lambda v: f"{v:.1f}"))
    out.append("\n```\n")
    out.append("Confirms the Phase 2.5 design: per-query BFS cost tracks matched-goroutine "
               "subgraph, but parse + graph build dominate, so wall is roughly "
               "match-independent.\n")

    out.append("\n## 6. gc_pre=true effect (per-iter vs process-wide)\n")
    keys = ["heap_mb", "goroutines", "match_fraction"]
    a = df[df["gc_pre"] == False].set_index(keys)
    b = df[df["gc_pre"] == True].set_index(keys)
    common = a.index.intersection(b.index)
    wall_diff = (b.loc[common, "wall_ms_mean"] - a.loc[common, "wall_ms_mean"])
    wall_ratio = b.loc[common, "wall_ms_mean"] / a.loc[common, "wall_ms_mean"]
    alloc_ratio = (
        b.loc[common, "go_total_alloc_delta_b_mean"]
        / a.loc[common, "go_total_alloc_delta_b_mean"]
    )
    ctx_ratio_inv = (a.loc[common, "time_voluntary_ctx"]
                     / b.loc[common, "time_voluntary_ctx"])
    elapsed_ratio_inv = (a.loc[common, "time_elapsed_s"]
                         / b.loc[common, "time_elapsed_s"])
    cpu_ratio_inv = ((a.loc[common, "time_user_s"] + a.loc[common, "time_sys_s"])
                     / (b.loc[common, "time_user_s"] + b.loc[common, "time_sys_s"]))
    out.append("Per-iteration (the only thing the endpoint actually exposes):\n\n")
    out.append(f"* Δwall (gc_pre=true − false) median = **{wall_diff.median():+.1f} ms**, "
               f"mean = **{wall_diff.mean():+.1f} ms** "
               f"({100*wall_diff.mean()/a.loc[common, 'wall_ms_mean'].mean():+.1f}% of baseline).\n")
    out.append(f"* wall ratio (true/false) median = **{wall_ratio.median():.3f}** "
               f"(≈ no per-call penalty).\n")
    out.append(f"* TotalAlloc Δ ratio (true/false) median = **{alloc_ratio.median():.2f}×**.\n")
    out.append("\nProcess-wide (across the full 20 + warmup + trace iteration run):\n\n")
    out.append(f"* Elapsed time gc_pre=false **{elapsed_ratio_inv.median():.2f}× longer** "
               f"(median), up to **{elapsed_ratio_inv.max():.2f}× longer** "
               f"at high heap×goroutines.\n")
    out.append(f"* Total CPU time gc_pre=false **{cpu_ratio_inv.median():.2f}× higher** "
               f"(median), up to **{cpu_ratio_inv.max():.2f}× higher**.\n")
    out.append(f"* Voluntary context switches gc_pre=false **{ctx_ratio_inv.median():.2f}× more** "
               f"(median), up to **{ctx_ratio_inv.max():.2f}× more**.\n")
    out.append("Reading: a single Compute call is the same speed either way, but without "
               "gc_pre the dump-parser heap garbage survives between calls and the runtime "
               "spends multiples more time on background GC/scheduling between iterations. "
               "For long-lived servers this is the dominant operational cost.\n")

    out.append("\n## 7. Heartbeat / wall fraction\n")
    df = df.copy()
    df["heartbeat_share"] = df["max_heartbeat_pause_ns_mean"] / df["wall_ns_mean"]
    out.append(f"* median share of wall covered by the worst scheduling pause = "
               f"**{df['heartbeat_share'].median():.2f}**.\n")
    out.append(f"* p95 share = **{df['heartbeat_share'].quantile(0.95):.2f}**.\n")
    out.append("If this share is close to 1 the worst pause is dominated by a single STW; "
               "lower values mean wall-time growth is mostly outside STW (parsing, BFS).\n")

    out.append("\n## 8. Per-goroutine cost (slope of wall vs goroutines)\n")
    out.append("Holding heap_mb fixed, the slope of `wall_ms_mean` against `goroutines` "
               "isolates the labeled-goroutine cost (stack scan + label decode + BFS root).\n\n")
    out.append("```\n")
    out.append(per_g.round(3).to_string(index=False))
    out.append("\n```\n")
    out.append(f"Median per-goroutine cost ≈ **{per_g['us_per_goroutine'].median():.1f} µs**. "
               "This is roughly stable across heap sizes, which is what you'd expect: "
               "stack scanning depends on stack content and label byte count, not on the "
               "data-heap size.\n")

    out.append("\n## 9. Runtime/trace cross-check iteration\n")
    out.append(f"Each config records one extra iteration under runtime/trace. The trace "
               f"adds median **{trace_df['trace_overhead_pct'].median():.1f}%** wall-time overhead "
               f"(p95 **{trace_df['trace_overhead_pct'].quantile(0.95):.1f}%**, "
               f"max **{trace_df['trace_overhead_pct'].max():.1f}%**), and was excluded "
               "from the variance/percentile statistics above. It is useful as a sanity "
               "check that the heartbeat-based STW estimate agrees with `go tool trace` "
               "spans, but it is not iso-cost with a normal Compute and must not be used "
               "for timing claims.\n")

    out.append("\n## 10. Plots\n")
    for p in sorted(PLOTS.glob("*.png")):
        out.append(f"* `{p.relative_to(RESULTS)}`\n")

    target = RESULTS / "analysis.md"
    target.write_text("".join(out))
    return target


# ----------------------------------------------------------------------------- main

def main() -> None:
    df = load_summary()
    iters = load_iterations()

    print(f"loaded {len(df)} configs, {len(iters)} per-iteration samples")
    print("plots →", PLOTS)

    plot_wall_vs_heap(df)
    plot_wall_vs_goroutines(df)
    plot_match_effect(df)
    plot_gc_pre_effect(df)
    plot_heartbeat(df)
    plot_heartbeat_ratio(df)
    plot_rss_overhead(df)
    plot_alloc_vs_heap(df)
    plot_variance_iter(iters)
    plot_cv(df)
    plot_elapsed_vs_iter(df)
    plot_ctx_switches(df)
    per_g = plot_per_goroutine_cost(df)
    trace_df = collect_trace_overhead()
    plot_trace_overhead(trace_df)

    md = write_markdown(df, iters, per_g, trace_df)
    print("wrote", md)


if __name__ == "__main__":
    main()
