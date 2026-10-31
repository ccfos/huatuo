---
title: Standalone SchedBlame
type: docs
description: "Run SchedBlame independently and record contention diagnostics."
author: HUATUO Team
date: 2026-10-26
weight: 8
---

The `sched-blame` command runs the SchedBlame tracer independently of the Huatuo
daemon. It uses the same target selection, BPF program, attribution, ratio
history, and diagnostics. Anomaly events are appended to a human-readable file;
no Elasticsearch or other storage backend is initialized.

## Build

The normal build discovers `cmd/sched-blame` automatically:

```bash
make build
```

The executable is `_output/bin/sched-blame`. Use `_output/bpf` for its BPF
objects, including those required for container discovery. Build these objects
on the target architecture.

If the BPF objects are already built, compile just the command with:

```bash
go build -o _output/bin/sched-blame ./cmd/sched-blame
```

The tracer supports Linux 4.18 through 6.5 with `CONFIG_FAIR_GROUP_SCHED=y` and
`CONFIG_CFS_BANDWIDTH=y`. Run with the privileges needed to load BPF and inspect
container cgroups.

## Run

```bash
sudo ./_output/bin/sched-blame \
    --config ./huatuo-bamai.conf \
    --bpf-dir ./_output/bpf \
    --output-file ./sched-blame-events.log \
    --log-debug
```

The command reads `Log`, `Pod`, and `AutoTracing` from the normal Huatuo TOML
configuration and ignores unrelated sections. `Pod` settings control kubelet
ports and client certificates exactly as for the daemon. For example:

```toml
[Pod]
    KubeletReadOnlyPort = 10255
    KubeletAuthorizedPort = 10250
    KubeletClientCertPath = "/path/client.pem,/path/client-key.pem"

[AutoTracing.SchedBlame]
    TargetContainerScope = "normal"
    SliceDropPercent = 0
    ExternalAnomalyK = 1.0
    SliceBatchSize = 128
    PerfEventPerCPUBufferBytes = 524288
    PerfEventWatermarkBytes = 8192
    PerfEventQueueRecords = 8192
    PollIntervalMs = 100
```

`--output-file` is required. Events include the target identity, external and
internal contention percentages, historical P99, anomaly threshold, runtime,
contention and throttle durations, and the top external competitors.
An unavailable internal percentage is `N/A`; a valid zero is `0.0000%`.
Anomalies require at least 60 valid previous external-ratio samples and a
positive P99 baseline. The event file is therefore not a per-second recording.

Operational logs and highlighted-container diagnostics use the normal Huatuo
logger: stdout by default, or `Log.File` when configured. Anomaly event data goes
only to the output file. `--log-debug` enables debug logging.

The command runs until SIGHUP, SIGQUIT, SIGINT, or SIGTERM. Normal shutdown uses
the tracer's final drain and event-upload queue to finish pending accounting
and file writes.

## Options

| Option | Meaning |
| --- | --- |
| `--config PATH` | TOML file; defaults to `../conf/huatuo-bamai.conf` relative to the executable directory. |
| `--bpf-dir PATH` | BPF object directory; defaults to `../bpf` relative to the executable directory. |
| `--output-file PATH` | Required append-only anomaly file. Parent directories are created when needed. |
| `--highlight-container VALUE` | Overrides `HighlightContainer` with a container ID, unique prefix, or hostname. |
| `--ratio-debug-file PATH` | Overrides `ExternalRatioDebugFile` with a per-second CSV path. |
| `--log-debug` | Enables debug logs and the configured highlight's diagnostics. |
| `--pprof-addr ADDRESS` | Optional HTTP listener for Go runtime profiles; disabled by default. |

Highlighting is optional. When configured, it must resolve at startup to one
container with a usable CPU cgroup. It reserves dense target slot zero and
bypasses the candidate scope and QoS filters. The remaining slots are selected
normally. Without a highlight, all slots remain available for normal selection.

All paths should be supplied through named options. Positional arguments are
rejected.

## Ratio Charts

To record ratios and show one container's debug diagnostics:

```bash
sudo ./_output/bin/sched-blame \
    --config ./huatuo-bamai.conf --bpf-dir ./_output/bpf \
    --output-file ./sched-blame-events.log \
    --highlight-container CONTAINER_ID \
    --ratio-debug-file ./ratios.csv --log-debug
```

The CSV records all active targets each second. The highlight also includes
SuperMonitor waitrate when its custom kernel counters are available. Missing
counters produce unavailable SuperMonitor values while SchedBlame continues.

```bash
python3 integration/testdata/sched_contention.py chart ratios.csv --html ratios.html
```

See [SchedBlame Analysis](sched-blame-analysis_en.md) for recording and comparison
options.

## CPU Profiling

Add `--pprof-addr 127.0.0.1:6060` to the command. Capture a text profile with:

```bash
curl -o sched-blame.cpu.pprof \
    'http://127.0.0.1:6060/debug/pprof/profile?seconds=120'
go tool pprof -top ./_output/bin/sched-blame sched-blame.cpu.pprof
```

For an interactive browser view:

```bash
go tool pprof -http=127.0.0.1:8080 \
    ./_output/bin/sched-blame sched-blame.cpu.pprof
```
