# HUATUO Performance-Overhead Benchmark

This directory contains the automation that **quantifies the performance impact
HUATUO imposes on the workloads it observes**. It resolves issue #336.

The methodology, metric definitions, and interpretation guidance live in
[`docs/best-practice/performance-overhead_en.md`](../docs/best-practice/performance-overhead_en.md).
This README is the operator guide.

## What it measures

| Scenario | Metric | Unit |
|----------|--------|------|
| `cpu_overhead` | extra wall time of a CPU-bound workload while huatuo collects | seconds (+ %) |
| `memory_overhead` | huatuo-bamai resident memory under sustained load | MiB |
| `net_latency` | incremental loopback RTT while huatuo collects | microseconds (+ %) |
| `io_latency` | incremental latency of synchronized writes while huatuo collects | milliseconds (+ %) |

Each metric is an A/B experiment (huatuo **off** vs **on**) repeated
`BENCH_ITERATIONS` times (plus one discarded warmup), reported with mean /
median / p95 / stddev / min / max. Every probe-heavy metric is run under
multiple **collection profiles**:

| Profile | BlackList | Meaning |
|---------|-----------|---------|
| `full` | shipped default | realistic multi-collector deployment |
| `minimal` | every optional collector off | lower bound of huatuo overhead |
| `single` (net/io only) | every optional collector off except one | isolates a single collector |

## Requirements

- root (`huatuo-bamai` needs `CAP_BPF`/`CAP_SYS_ADMIN`)
- `huatuo-bamai` built (`make all` → `_output/bin/huatuo-bamai`)
- `curl`, `dd`, `ping`, `awk`, `sort`, `python3` (optional, for pretty-printing
  and the regression gate)

When a precondition is not met the harness records a `"status": "skipped"`
result with a reason instead of failing, so a CI lane never turns red solely
because it cannot run the benchmark.

## Quick start

```bash
make bench                 # build huatuo-bamai, then run the full benchmark
# or, against an existing build:
bash bench/run.sh
```

The report is written to `bench/results/bench-<UTC-timestamp>.json` and a text
summary to `bench/results/bench-<UTC-timestamp>.summary.txt`. The summary is
also printed to stdout.

## Knobs

All knobs are environment variables (defaults in `env.sh`).

| Variable | Default | Meaning |
|----------|---------|---------|
| `HUATUO_BAMAI_BIN` | `_output/bin/huatuo-bamai` | binary under test |
| `BENCH_ITERATIONS` | `5` | A/B repetitions per scenario |
| `BENCH_WARMUP` | `1` | leading iterations discarded |
| `BENCH_CPU_BYTES` | `67108864` (64 MiB) | bytes copied per CPU sample |
| `BENCH_MEM_DURATION` | `15` | RSS sampling window (seconds) |
| `BENCH_NET_PACKETS` | `200` | loopback pings per sample |
| `BENCH_IO_OPS` | `200` | sync writes per sample |
| `BENCH_IO_BLOCK_KIB` | `4` | write block size (KiB) |
| `BENCH_NET_SINGLE_MODULE` | `dropwatch` | module isolated by the net `single` profile |
| `BENCH_IO_SINGLE_MODULE` | `iolatency` | module isolated by the io `single` profile |
| `BENCH_RESULTS_DIR` | `bench/results` | output directory |
| `BENCH_FAIL_ON_REGRESSION` | `0` | exit non-zero on threshold breach |
| `BENCH_FAIL_ON_MISSING` | `0` | exit non-zero on missing preconditions |
| `BENCH_THRESHOLD_CPU_PCT` | `5` | regression gate: max CPU overhead % |
| `BENCH_THRESHOLD_NET_PCT` | `10` | regression gate: max net latency % |
| `BENCH_THRESHOLD_IO_PCT` | `10` | regression gate: max IO latency % |

Examples:

```bash
# fast smoke run
BENCH_ITERATIONS=2 BENCH_NET_PACKETS=50 BENCH_IO_OPS=50 bash bench/run.sh

# strict CI gate
BENCH_FAIL_ON_REGRESSION=1 bash bench/run.sh
```

## Multi-scenario coverage

- **Idle vs load** — CPU/memory scenarios drive the system under load. To
  capture an explicit idle baseline, quiesce other processes and re-run; the
  JSON `metadata` lets you compare runs.
- **Single vs multi module** — the `single` profile (net/io) isolates one
  collector; `full` runs them all.
- **Container vs host** — `metadata.env` records whether the run was inside a
  container. Run the script once on the host and once in a container and
  compare the two JSON reports.

## CI integration

`.github/workflows/benchmark.yml` builds `huatuo-bamai`, runs this benchmark in
a QEMU VM, and uploads the JSON + summary as workflow artifacts. It triggers on
release tags and on manual dispatch, so it never blocks ordinary pull requests.
Set `BENCH_FAIL_ON_REGRESSION=1` in the workflow (or via dispatch input) to
turn threshold breaches into CI failures.

## Layout

```
bench/
├── env.sh               # defaults / tunables / module model
├── lib.sh               # logging, stats, JSON, huatuo lifecycle, workloads
├── run.sh               # orchestrator (entry point)
├── scenario_cpu.sh      # CPU overhead
├── scenario_memory.sh   # huatuo-bamai RSS
├── scenario_net.sh      # network RTT overhead
├── scenario_io.sh       # IO latency overhead
└── results/             # generated reports (gitignored)
```
