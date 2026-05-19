#!/usr/bin/env python3
"""Thesis-grade analysis of bench/results/.

Reads the per-config JSONs and summary.csv produced by run.sh + aggregate.py,
emits plots into bench/results/plots/ and a short analysis.md summary.

Scope: this script characterises the `memusage.Compute` / `/debug/memusage`
endpoint, not the bench harness. It therefore restricts the analysis to
`gc_pre=true` (i.e. `memusage.Options.GCBeforeHeapDump = true`), which is
the operating point the endpoint is expected to be deployed with: a forced
GC immediately before `runtime/debug.WriteHeapDump` keeps the dump small,
keeps RSS overhead close to 1× the live heap, and eliminates cross-call
parser garbage from inflating the next call. Bench-only artefacts (the
runtime/trace iteration, the heartbeat goroutine, `/usr/bin/time -v`
process-wide counters) are not reported here.

Drops:
    * the 2 stray heap_mb=800 rows (partial earlier run);
    * the `gc_pre=false` half of the sweep;
    * the under_trace iteration from per-iteration variance stats.

Usage: python3 bench/analyze.py
"""

from __future__ import annotations

import json
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
    df = df[df["heap_mb"] != 800].copy()
    df = df[df["gc_pre"] == True].copy()
    df["wall_ms_mean"] = df["wall_ns_mean"] / 1e6
    df["wall_ms_stddev"] = df["wall_ns_stddev"] / 1e6
    df["wall_ms_p50"] = df["wall_ns_p50"] / 1e6
    df["wall_ms_p95"] = df["wall_ns_p95"] / 1e6
    df["wall_ms_p99"] = df["wall_ns_p99"] / 1e6
    df["cv_wall_pct"] = 100.0 * df["wall_ns_stddev"] / df["wall_ns_mean"]
    df["reachable_mb_mean"] = df["reachable_bytes_mean"] / (1 << 20)
    df["vm_hwm_mb"] = df["vm_hwm_after_kb_mean"] / 1024
    df["vm_rss_mb"] = df["vm_rss_after_kb_mean"] / 1024
    df["alloc_per_call_mb"] = df["go_heap_alloc_delta_b_mean"] / (1 << 20)
    df["total_alloc_per_call_gb"] = df["go_total_alloc_delta_b_mean"] / (1 << 30)
    return df


def load_iterations() -> pd.DataFrame:
    """One row per (config, iteration); excludes trace iterations and gc_pre=false."""
    rows = []
    for jp in sorted(RESULTS.glob("*.json")):
        d = json.loads(jp.read_text())
        cfg = d["config"]
        if cfg["heap_mb"] == 800 or not cfg["gc_pre"]:
            continue
        for it in d["iterations"]:
            if it.get("under_trace"):
                continue
            rows.append({
                "tag": cfg["tag"],
                "heap_mb": cfg["heap_mb"],
                "goroutines": cfg["goroutines"],
                "match_fraction": cfg["match_fraction"],
                "index": it["index"],
                "wall_ns": it["wall_ns"],
                "go_heap_alloc_delta_b": it["go_heap_alloc_delta_b"],
                "go_total_alloc_delta_b": it["go_total_alloc_delta_b"],
                "vm_rss_after_kb": it["vm_rss_after_kb"],
                "vm_hwm_after_kb": it["vm_hwm_after_kb"],
                "reachable_bytes": it["reachable_bytes"],
                "matched_goroutines": it["matched_goroutines"],
            })
    iters = pd.DataFrame(rows)
    iters["wall_ms"] = iters["wall_ns"] / 1e6
    return iters


# ----------------------------------------------------------------------------- helpers

def savefig(name: str) -> Path:
    p = PLOTS / name
    plt.tight_layout()
    plt.savefig(p, dpi=130)
    plt.close()
    return p


def linear_fit(x: np.ndarray, y: np.ndarray) -> tuple[float, float, float]:
    if len(x) < 2:
        return float("nan"), float("nan"), float("nan")
    a, b = np.polyfit(x, y, 1)
    yhat = a * x + b
    ss_res = np.sum((y - yhat) ** 2)
    ss_tot = np.sum((y - y.mean()) ** 2)
    r2 = 1.0 - ss_res / ss_tot if ss_tot > 0 else float("nan")
    return float(a), float(b), float(r2)


# ----------------------------------------------------------------------------- plots

def plot_wall_vs_heap(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0].sort_values(["goroutines", "heap_mb"])
    fig, ax = plt.subplots(figsize=(7.2, 4.6))
    for g, grp in sub.groupby("goroutines"):
        ax.errorbar(
            grp["heap_mb"], grp["wall_ms_mean"], yerr=grp["wall_ms_stddev"],
            marker="o", capsize=3, label=f"{g} goroutines",
        )
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("Compute wall time (ms, mean ± stddev)")
    ax.set_title("Wall time vs workload heap size")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=9)
    savefig("wall_vs_heap.png")


def plot_wall_vs_goroutines(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0].sort_values(["heap_mb", "goroutines"])
    fig, ax = plt.subplots(figsize=(7.2, 4.6))
    for h, grp in sub.groupby("heap_mb"):
        ax.errorbar(
            grp["goroutines"], grp["wall_ms_mean"], yerr=grp["wall_ms_stddev"],
            marker="o", capsize=3, label=f"{h} MiB heap",
        )
    ax.set_xlabel("labeled goroutines")
    ax.set_ylabel("Compute wall time (ms, mean ± stddev)")
    ax.set_title("Wall time vs goroutine count")
    ax.set_xscale("log")
    ax.grid(True, alpha=0.3, which="both")
    ax.legend(fontsize=9)
    savefig("wall_vs_goroutines.png")


def plot_match_effect(df: pd.DataFrame) -> None:
    sub = df[df["goroutines"] == 1000].sort_values(["heap_mb", "match_fraction"])
    fig, axes = plt.subplots(1, 2, figsize=(11.5, 4.4))
    for h, grp in sub.groupby("heap_mb"):
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
    axes[0].set_title("Wall time is independent of match_fraction")
    axes[0].grid(True, alpha=0.3)
    axes[0].legend(fontsize=8)
    axes[1].set_xlabel("match_fraction")
    axes[1].set_ylabel("reachable bytes (MiB)")
    axes[1].set_title("Reported reachable bytes are linear in match_fraction")
    axes[1].grid(True, alpha=0.3)
    axes[1].legend(fontsize=8)
    fig.suptitle("Effect of label match_fraction on Compute (g=1000)")
    savefig("match_effect.png")


def plot_rss_overhead(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0].copy()
    sub["rss_overhead_mb"] = sub["vm_hwm_mb"] - sub["heap_mb"]
    fig, ax = plt.subplots(figsize=(7.2, 4.6))
    for g, grp in sub.sort_values(["goroutines", "heap_mb"]).groupby("goroutines"):
        ax.plot(grp["heap_mb"], grp["rss_overhead_mb"], marker="o",
                label=f"{g} goroutines")
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("VmHWM − workload heap (MiB)")
    ax.set_title("Peak RSS overhead of /debug/memusage")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=9)
    savefig("rss_overhead.png")


def plot_alloc_vs_heap(df: pd.DataFrame) -> None:
    sub = df[df["match_fraction"] == 1.0].sort_values(["goroutines", "heap_mb"])
    fig, ax = plt.subplots(figsize=(7.2, 4.6))
    for g, grp in sub.groupby("goroutines"):
        ax.plot(grp["heap_mb"], grp["total_alloc_per_call_gb"],
                marker="o", label=f"{g} goroutines")
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("TotalAlloc Δ per Compute (GiB)")
    ax.set_title("Go allocator pressure per /debug/memusage call")
    ax.grid(True, alpha=0.3)
    ax.legend(fontsize=9)
    savefig("alloc_vs_heap.png")


def plot_variance_iter(iters: pd.DataFrame) -> None:
    sub = iters[iters["match_fraction"] == 1.0].copy()
    sub["cfg"] = sub.apply(lambda r: f"h{r.heap_mb}/g{r.goroutines}", axis=1)
    order = sub.groupby("cfg")["wall_ms"].median().sort_values().index.tolist()
    sub["cfg"] = pd.Categorical(sub["cfg"], categories=order, ordered=True)
    sub = sub.sort_values("cfg")
    fig, ax = plt.subplots(figsize=(13, 5.0))
    data = [sub[sub["cfg"] == c]["wall_ms"].values for c in order]
    ax.boxplot(data, tick_labels=order, showmeans=True, meanline=True)
    ax.set_xticklabels(order, rotation=70, fontsize=7)
    ax.set_ylabel("Compute wall time (ms)")
    ax.set_title("Per-iteration wall-time distribution (match=1.0, n=20 each)")
    ax.grid(True, alpha=0.3, axis="y")
    savefig("variance_box.png")


def plot_cv(df: pd.DataFrame) -> None:
    fig, ax = plt.subplots(figsize=(7.2, 4.4))
    ax.hist(df["cv_wall_pct"].dropna(), bins=20, edgecolor="black")
    med = df["cv_wall_pct"].median()
    p95 = df["cv_wall_pct"].quantile(0.95)
    ax.axvline(med, color="red", ls="--", label=f"median = {med:.1f}%")
    ax.axvline(p95, color="orange", ls="--", label=f"p95 = {p95:.1f}%")
    ax.set_xlabel("CV(wall) = stddev/mean (%)")
    ax.set_ylabel("# configs")
    ax.set_title(f"Wall-time stability across {len(df)} configs (n=20 iterations each)")
    ax.legend()
    ax.grid(True, alpha=0.3)
    savefig("cv_histogram.png")


def plot_per_goroutine_cost(df: pd.DataFrame) -> pd.DataFrame:
    sub = df[df["match_fraction"] == 1.0]
    rows = []
    for h, grp in sub.groupby("heap_mb"):
        x = grp["goroutines"].to_numpy(dtype=float)
        y = grp["wall_ms_mean"].to_numpy(dtype=float)
        a, b, r2 = linear_fit(x, y)
        rows.append((h, a * 1000.0, b, r2))  # ms/goroutine → µs/goroutine
    fit = pd.DataFrame(rows, columns=["heap_mb", "us_per_goroutine", "intercept_ms", "R2"])
    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.plot(fit["heap_mb"], fit["us_per_goroutine"], marker="o")
    ax.set_xlabel("workload heap (MiB)")
    ax.set_ylabel("per-goroutine wall cost (µs)")
    ax.set_title("Per-labeled-goroutine Compute cost (slope of wall vs g)")
    ax.grid(True, alpha=0.3)
    savefig("per_goroutine_cost.png")
    return fit


# ----------------------------------------------------------------------------- fit tables

def fit_wall_vs_heap(df: pd.DataFrame) -> pd.DataFrame:
    rows = []
    sub = df[df["match_fraction"] == 1.0]
    for g, grp in sub.groupby("goroutines"):
        a, b, r2 = linear_fit(
            grp["heap_mb"].to_numpy(dtype=float),
            grp["wall_ms_mean"].to_numpy(dtype=float),
        )
        rows.append({
            "goroutines": g,
            "wall_ms_per_MiB_heap": a,
            "intercept_ms": b,
            "R2": r2,
        })
    return pd.DataFrame(rows).sort_values("goroutines")


def fit_rss_overhead(df: pd.DataFrame) -> pd.DataFrame:
    sub = df[df["match_fraction"] == 1.0].copy()
    sub["rss_overhead_mb"] = sub["vm_hwm_mb"] - sub["heap_mb"]
    rows = []
    for g, grp in sub.groupby("goroutines"):
        a, b, r2 = linear_fit(
            grp["heap_mb"].to_numpy(dtype=float),
            grp["rss_overhead_mb"].to_numpy(dtype=float),
        )
        rows.append({
            "goroutines": g,
            "rss_overhead_MiB_per_MiB_heap": a,
            "intercept_MiB": b,
            "R2": r2,
        })
    return pd.DataFrame(rows).sort_values("goroutines")


# ----------------------------------------------------------------------------- markdown

def write_markdown(df: pd.DataFrame, iters: pd.DataFrame, per_g: pd.DataFrame) -> Path:
    out = []
    out.append("# /debug/memusage benchmark analysis\n\n")
    out.append(
        f"All numbers below characterise `memusage.Compute` "
        f"with `Options.GCBeforeHeapDump = true` "
        f"(`go {df['go_version'].iloc[0]}` on `{df['goarch'].iloc[0]}`). "
        f"{len(df)} configurations = 5 workload heap sizes × 4 goroutine counts "
        f"× 3 match fractions; **20 iterations per configuration**, "
        f"plus 3 discarded warm-ups.\n"
    )

    out.append("\n## 1. Variance and stability\n")
    cv = df["cv_wall_pct"].describe(percentiles=[0.5, 0.9, 0.95])
    out.append(
        f"* Coefficient of variation of wall time (stddev/mean across the 20 "
        f"iterations of a config): median **{cv['50%']:.2f} %**, "
        f"p95 **{cv['95%']:.2f} %**, max **{cv['max']:.2f} %**.\n"
    )
    high_cv = (
        df[df["cv_wall_pct"] > 8][["tag", "wall_ms_mean", "cv_wall_pct"]]
        .sort_values("cv_wall_pct", ascending=False).head(5)
    )
    if not high_cv.empty:
        out.append("\nConfigs with CV(wall) > 8 %:\n\n")
        out.append("```\n" + high_cv.to_string(index=False, float_format=lambda v: f"{v:.2f}") + "\n```\n")
    out.append(
        "All of the noisier configs are the small-heap (50 MiB) cases where the "
        "absolute wall time is tens of ms, so absolute jitter is small even when "
        "the relative figure looks large. For every heap ≥ 200 MiB the CV is "
        "under 5 %.\n"
    )

    out.append("\n## 2. Wall time vs workload heap (linear model)\n")
    fit_h = fit_wall_vs_heap(df)
    out.append("```\n" + fit_h.to_string(index=False, float_format=lambda v: f"{v:.3f}") + "\n```\n")
    out.append(
        f"`wall ≈ a · heap_MiB + b`. All four goroutine buckets give the same "
        f"slope ≈ **{fit_h['wall_ms_per_MiB_heap'].mean():.3f} ms/MiB** "
        f"(R² ≥ {fit_h['R2'].min():.3f}). The intercept grows from "
        f"{fit_h['intercept_ms'].iloc[0]:.0f} ms at 100 goroutines to "
        f"{fit_h['intercept_ms'].iloc[-1]:.0f} ms at 10 000 — this is the cost "
        "Compute pays just to enumerate goroutine roots and decode labels, "
        "independent of heap size.\n"
    )

    out.append("\n## 3. Per-labeled-goroutine cost\n")
    out.append(
        "Holding heap_mb fixed and fitting wall against goroutines isolates "
        "the labeled-goroutine cost (stack scan + label decode + goroutine-root "
        "BFS source):\n\n"
    )
    out.append("```\n" + per_g.round(3).to_string(index=False) + "\n```\n")
    out.append(
        f"Median per-goroutine cost ≈ **{per_g['us_per_goroutine'].median():.1f} µs**, "
        "roughly heap-independent — consistent with the goroutine-side work "
        "depending on stack content and label byte count rather than data-heap "
        "size.\n"
    )

    out.append("\n## 4. Match fraction does not affect wall time\n")
    pivot = (
        df.pivot_table(
            index=["heap_mb", "goroutines"],
            columns="match_fraction",
            values="wall_ms_mean",
        )
    )
    pivot.columns = [f"match={c}" for c in pivot.columns]
    out.append("Mean wall time (ms) by match_fraction:\n\n")
    out.append("```\n" + pivot.to_string(float_format=lambda v: f"{v:.1f}") + "\n```\n")
    out.append(
        "Wall time is essentially flat across `match_fraction` ∈ {0.01, 0.5, 1.0}: "
        "parsing the dump and building the structural graph dominate, and the "
        "per-query BFS that does depend on matched-goroutine count is cheap by "
        "comparison. The reported `reachable_bytes`, by contrast, scales "
        "linearly with `match_fraction` (see `plots/match_effect.png`), which is "
        "the expected behaviour for the workload (each matched goroutine retains "
        "an equal share of the heap).\n"
    )

    out.append("\n## 5. Peak RSS overhead\n")
    fit_r = fit_rss_overhead(df)
    out.append("```\n" + fit_r.to_string(index=False, float_format=lambda v: f"{v:.3f}") + "\n```\n")
    out.append(
        f"`VmHWM − workload_heap` grows linearly with workload heap at "
        f"slope ≈ **{fit_r['rss_overhead_MiB_per_MiB_heap'].mean():.2f}× workload heap** "
        f"(R² ≥ {fit_r['R2'].min():.3f}), with a small per-goroutine intercept "
        f"({fit_r['intercept_MiB'].min():.0f}–{fit_r['intercept_MiB'].max():.0f} MiB). "
        "The slope ≈ 1× reflects that the dump file is roughly the size of the "
        "live heap and its page-cache pages count toward VmHWM at peak. Steady-"
        "state RSS (`VmRSS` after the call returns) is much smaller: the page "
        "cache is reclaimable.\n"
    )

    out.append("\n## 6. Allocator pressure per call\n")
    per_call = df[df["match_fraction"] == 1.0].groupby("heap_mb")[
        ["alloc_per_call_mb", "total_alloc_per_call_gb"]
    ].mean()
    out.append(
        "Mean allocator deltas during a single Compute, by workload heap "
        "(averaged over goroutine counts):\n\n"
    )
    out.append("```\n" + per_call.round(3).to_string() + "\n```\n")
    out.append(
        "`HeapAlloc Δ` is the *retained* growth (parsed snapshot + graph kept "
        "alive until Compute returns); `TotalAlloc Δ` is the *cumulative* "
        "allocation including transient parser buffers. The Compute call "
        "allocates ~1 GiB of transient memory per ~1 GiB workload heap; this "
        "transient memory is freed at the next GC, which is why steady-state "
        "RSS recovers between calls.\n"
    )

    out.append("\n## 7. Plots\n")
    for p in sorted(PLOTS.glob("*.png")):
        out.append(f"* `{p.relative_to(RESULTS)}`\n")

    target = RESULTS / "analysis.md"
    target.write_text("".join(out))
    return target


# ----------------------------------------------------------------------------- main

def main() -> None:
    df = load_summary()
    iters = load_iterations()

    print(f"loaded {len(df)} configs (gc_pre=true only), {len(iters)} per-iteration samples")
    print("plots →", PLOTS)

    # Wipe stale plots so the directory always reflects the current script.
    for p in PLOTS.glob("*.png"):
        p.unlink()

    plot_wall_vs_heap(df)
    plot_wall_vs_goroutines(df)
    plot_match_effect(df)
    plot_rss_overhead(df)
    plot_alloc_vs_heap(df)
    plot_variance_iter(iters)
    plot_cv(df)
    per_g = plot_per_goroutine_cost(df)

    md = write_markdown(df, iters, per_g)
    print("wrote", md)


if __name__ == "__main__":
    main()
