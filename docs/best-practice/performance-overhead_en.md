---
title: Performance Overhead Benchmark
type: docs
description: "Methodology and tooling for measuring the performance impact HUATUO imposes on observed workloads."
author: HUATUO Team
date: 2026-07-12
weight: 6
---

## Why measure overhead

HUATUO is an eBPF-based observability agent. Every kernel probe it attaches, every
ring buffer it drains, and every metric it scrapes adds work to the system it
observes. This document describes the **systematic methodology** and the
**automation** under `bench/` that quantifies that overhead so users can make an
informed deploy/no-deploy decision for their own environment.

The benchmark answers four questions:

| Metric | Question answered | Reported unit |
|--------|-------------------|---------------|
| **CPU overhead** | How much extra CPU does the observed workload burn while HUATUO is collecting? | percent increase of workload CPU/wall time |
| **Memory overhead** | How much resident memory does the `huatuo-bamai` process itself consume? | MiB (RSS, peak `VmHWM`) |
| **Network latency** | How much per-packet latency does HUATUO add to the receive path? | microseconds (added RTT) |
| **IO latency** | How much per-operation latency does HUATUO add to the block-IO path? | milliseconds (added latency) |

---

## Methodology

### A/B with paired statistics

Every metric is measured as a controlled **A/B experiment**:

1. **Baseline (A)** — the workload runs with `huatuo-bamai` *stopped*.
2. **Observed (B)** — the same workload runs with `huatuo-bamai` *started* and
   collecting.

Each side is repeated `BENCH_ITERATIONS` times (default `5`). The harness
discards the first iteration as a warmup, then reports for each side: sample
count, min, mean, median, p95, max and standard deviation. The headline numbers
are:

- **`delta_mean`** = `mean(B) − mean(A)`
- **`delta_percent`** = `delta_mean / mean(A) × 100`

Reporting the full distribution (not a single number) makes the result
reproducible and lets a reviewer judge whether a difference is within noise.

### Workloads

The workloads are intentionally built from tools that exist on every Linux
distribution (`dd`, `ping`, `/proc`), so the benchmark has **no extra build
dependencies** and runs identically in CI and on a production host.

| Metric | Workload | What it exercises |
|--------|----------|-------------------|
| CPU overhead | `dd if=/dev/zero of=/dev/null bs=64K` for a fixed byte count | CPU + memcpy; visible to HUATUO's CPU/scheduling probes |
| Memory overhead | sustained mixed CPU+IO load window | HUATUO resident set under steady state |
| Network latency | `ping -c N 127.0.0.1` over loopback | net RX path instrumented by `dropwatch`/`netrxlatency`/`netdev` |
| IO latency | small `oflag=dsync` writes in a loop | block-IO path instrumented by `iolatency`/`iotracing` |

Relative (A/B) workloads are short and matched in size so the baseline and
observed phases experience the same thermal/scheduling conditions.

### Module matrix (single vs multi)

`huatuo-bamai` enables/disables collectors through its `BlackList` config. The
benchmark therefore runs each metric under up to **three collection profiles**:

| Profile | BlackList | Meaning |
|---------|-----------|---------|
| **full** | the shipped `huatuo-bamai.conf` (`netdev_hw`, `metax_gpu`, `ascend_npu`) | realistic multi-collector deployment — worst-case overhead |
| **minimal** | core hardware + every optional software collector | every optional collector off — lower bound of huatuo overhead |
| **single** (net/io) | core + every optional collector except the one relevant to the metric | isolates a single collector's contribution |

The optional collectors exercised are: `softlockup`, `ethtool`, `netstat_hw`,
`iolatency`, `memory_free`, `memory_reclaim`, `reschedipi`, `softirq`,
`iotracing`, `dropwatch`, `netdev_hw`.

For the network scenario the single profile isolates `dropwatch`; for the IO
scenario it isolates `iolatency`. This produces a direct **single vs multi**
comparison per metric, as required by the issue.

### Environment (idle vs load, container vs host)

- **Idle vs load** — the CPU and memory scenarios drive the system under load
  (a CPU-bound `dd` workload; a sustained mixed CPU+IO window for the memory
  scenario). To capture an explicit idle baseline, quiesce other processes on
  the host and re-run; the JSON `metadata` block lets you compare the two runs.
- **Container vs host** — the harness auto-detects whether it runs inside a
  container (overlay/btrfs root or `systemd-detect-virt -c`) and records the
  result under `metadata.env`. Run the same script once on the host and once in
  a container to compare; the JSON metadata makes the two runs comparable.

---

## Output format

`bench/run.sh` writes a single machine-readable JSON document to
`bench/results/bench-<timestamp>.json` and prints a human-readable summary to
stdout. The JSON schema (versioned):

```json
{
  "version": 1,
  "metadata": {
    "kernel": "6.8.0-124-generic",
    "arch": "x86_64",
    "ncpu": 8,
    "mem_total_kb": 16384000,
    "env": "host",
    "huatuo_version": "2.2.0",
    "iterations": 5,
    "timestamp_utc": "2026-07-12T09:30:00Z"
  },
  "scenarios": {
    "cpu_overhead": {
      "status": "ok",
      "unit": "seconds",
      "profiles": {
        "full":    { "baseline": { "...": "..." }, "observed": { "...": "..." }, "delta_mean": 0.03, "delta_percent": 1.2, "huatuo_cpu_seconds": 0.5 },
        "minimal": { "...": "..." }
      }
    },
    "memory_overhead": { "status": "ok", "unit": "MiB", "profiles": { "full": { "rss_peak_mib": 42.1 }, "minimal": { "rss_peak_mib": 38.0 } } },
    "net_latency":     { "status": "ok", "unit": "microseconds", "profiles": { "full": { "...": "..." }, "minimal": { "...": "..." }, "single": { "...": "..." } } },
    "io_latency":      { "status": "ok", "unit": "milliseconds", "profiles": { "full": { "...": "..." }, "minimal": { "...": "..." }, "single": { "...": "..." } } }
  }
}
```

A scenario reports `"status": "skipped"` with a `reason` when a precondition
(binary, root, kernel feature) is not met, so a CI lane never fails purely
because it cannot run the benchmark.

---

## Running

```bash
make bench                 # build huatuo-bamai, then run the benchmark
# or, against an existing build:
BENCH_ITERATIONS=10 bash bench/run.sh
```

See `bench/README.md` for all knobs (iterations, durations, thresholds, single
modules, output directory).

### CI integration

The `benchmark.yml` workflow builds `huatuo-bamai`, runs the benchmark in a QEMU
VM, uploads the JSON result plus a text summary as a build artifact, and (when
`BENCH_FAIL_ON_REGRESSION=1`) fails if any `delta_percent` crosses its configured
threshold. It runs on release tags and on manual dispatch so it never blocks
ordinary pull requests.

---

## Interpreting results

- **CPU overhead** below ~1–2% and **net/IO latency** deltas in the low
  single-digit percent are consistent with HUATUO's design goal of sub-1% probe
  overhead. Treat the measured value as an upper bound: the benchmark drives a
  deliberately probe-heavy workload.
- Results are **host-specific**. Always compare runs that share the same
  `metadata` (kernel, ncpu, env). The JSON makes side-by-side comparison easy.
- Use the **single** profile to attribute overhead to a specific collector when
  the **full** profile shows a regression.
